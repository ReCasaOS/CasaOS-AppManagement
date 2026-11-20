package v2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/containerfs"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"
	"github.com/labstack/echo/v4"
)

// The files of a running container, read from the host (see pkg/containerfs). Read only:
// listing, a text file, a download. The owner's alone: the Gateway keeps these routes from
// every other account, and the handlers refuse them again.

const (
	containerFilesMax   = 5000
	containerTextMax    = 1 << 20
	containerNotRunning = "the app is not running: start it to see its files"
)

// errContainerStopped is a container that has no process to read the files through.
var errContainerStopped = errors.New(containerNotRunning)

// containerOf is what the routes need of a running container besides its files.
type containerOf struct {
	mounts  []string // what an update keeps
	project string   // the compose app it belongs to, "" for a container of its own
}

// openContainerRoot is the filesystem of a running container and what else the routes
// need of it. A variable so a test hands a folder of its own.
var openContainerRoot = func(ctx context.Context, id string) (*containerfs.Root, containerOf, error) {
	inspected, err := service.MyService.Docker().DescribeContainer(ctx, id)
	if err != nil {
		return nil, containerOf{}, err
	}
	if inspected.State == nil || !inspected.State.Running || inspected.State.Pid <= 0 {
		return nil, containerOf{}, errContainerStopped
	}
	// what an update keeps: a volume or a folder of the box, not a tmpfs
	mounts := []string{}
	for _, m := range inspected.Mounts {
		if m.Type == mount.TypeBind || m.Type == mount.TypeVolume {
			mounts = append(mounts, path.Clean(m.Destination))
		}
	}
	root, err := containerfs.OfProcess(inspected.State.Pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil, containerOf{}, errContainerStopped // gone between the two
	}
	if err != nil {
		return nil, containerOf{}, err
	}
	// The pid was read before the root was opened. Had the container stopped in between
	// and the pid gone to a process of the box, the root would be the box's own: asked
	// again, the same container must still run under the same pid since the same start.
	again, err := service.MyService.Docker().DescribeContainer(ctx, id)
	if err != nil || again.State == nil || !again.State.Running || again.State.Pid != inspected.State.Pid || again.State.StartedAt != inspected.State.StartedAt {
		root.Close()
		return nil, containerOf{}, errContainerStopped
	}

	return root, containerOf{mounts: mounts, project: composeProjectOf(inspected)}, nil
}

// ContainerFiles lists a folder of a running container.
func (a *AppManagement) ContainerFiles(ctx echo.Context, id codegen.ContainerID, params codegen.ContainerFilesParams) error {
	root, mounts, dir, err := containerPath(ctx, string(id), params.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer root.Close()

	entries, truncated, err := root.List(dir, containerFilesMax)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	users, groups := root.Names("/etc/passwd"), root.Names("/etc/group")
	out := codegen.ContainerFiles{Path: dir, Entries: []codegen.ContainerFileEntry{}, Truncated: truncated, Volumes: mounts}
	if in := mountOf(mounts, dir); in != "" {
		out.InVolume = &in
	}
	for _, e := range entries {
		entry := codegen.ContainerFileEntry{
			Name:     e.Name,
			Type:     codegen.Other,
			Size:     e.Size,
			Mode:     modeText(e.Mode),
			Octal:    utils.Ptr(modeOctal(e.Mode)),
			Uid:      e.UID,
			Gid:      e.GID,
			Modified: e.Modified,
		}
		switch {
		case e.IsDir:
			entry.Type = codegen.Dir
			if contains(mounts, path.Join(dir, e.Name)) {
				entry.Volume = utils.Ptr(true)
			}
		case e.IsLink:
			entry.Type, entry.Link = codegen.Symlink, utils.Ptr(e.Link)
		case e.Mode&fs.ModeType == 0:
			entry.Type = codegen.File
		}
		if name, ok := users[e.UID]; ok {
			entry.User = utils.Ptr(name)
		}
		if name, ok := groups[e.GID]; ok {
			entry.Group = utils.Ptr(name)
		}
		out.Entries = append(out.Entries, entry)
	}

	return ctx.JSON(http.StatusOK, codegen.ContainerFilesOK{Message: utils.Ptr("OK"), Data: &out})
}

// ContainerFileContent answers a text file of a running container, up to 1 MB.
func (a *AppManagement) ContainerFileContent(ctx echo.Context, id codegen.ContainerID, params codegen.ContainerFileContentParams) error {
	root, _, p, err := containerPath(ctx, string(id), params.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer root.Close()

	f, info, err := root.OpenFile(p)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer f.Close()
	if info.Size() > containerTextMax {
		message := "the file is over 1 MB: download it instead"
		return ctx.JSON(http.StatusRequestEntityTooLarge, codegen.ResponseBadRequest{Message: &message})
	}
	data, err := io.ReadAll(io.LimitReader(f, containerTextMax+1))
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	if len(data) > containerTextMax {
		message := "the file is over 1 MB: download it instead"
		return ctx.JSON(http.StatusRequestEntityTooLarge, codegen.ResponseBadRequest{Message: &message})
	}
	if !isText(data) {
		message := "the file is not text: download it instead"
		return ctx.JSON(http.StatusUnsupportedMediaType, codegen.ResponseBadRequest{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.ContainerFileContentOK{
		Message: utils.Ptr("OK"),
		Data:    &codegen.ContainerFileContent{Path: p, Content: string(data), Size: int64(len(data))},
	})
}

// ContainerFileDownload sends one file of a running container as it is.
func (a *AppManagement) ContainerFileDownload(ctx echo.Context, id codegen.ContainerID, params codegen.ContainerFileDownloadParams) error {
	root, _, p, err := containerPath(ctx, string(id), params.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer root.Close()

	f, info, err := root.OpenFile(p)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer f.Close()

	res := ctx.Response()
	res.Header().Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(path.Base(p))))
	http.ServeContent(res, ctx.Request(), path.Base(p), info.ModTime(), f)

	return nil
}

// containerPath checks the caller and the path, and opens the container's filesystem.
func containerPath(ctx echo.Context, id, raw string) (*containerfs.Root, []string, string, error) {
	root, of, p, err := containerTarget(ctx, id, raw)

	return root, of.mounts, p, err
}

func containerTarget(ctx echo.Context, id, raw string) (*containerfs.Root, containerOf, string, error) {
	if !isOwner(ctx) {
		return nil, containerOf{}, "", errNotOwner
	}
	p, err := containerfs.Clean(raw)
	if err != nil {
		return nil, containerOf{}, "", errBadContainerPath{err}
	}
	root, of, err := openContainerRoot(ctx.Request().Context(), id)
	if err != nil {
		return nil, containerOf{}, "", err
	}
	sort.Strings(of.mounts)

	return root, of, p, nil
}

// containerChange opens the container for a change at raw, and holds its app the while:
// nothing is written while the app is updated, backed up or removed. done lets both go.
func containerChange(ctx echo.Context, id, raw string) (root *containerfs.Root, p string, done func(), err error) {
	root, of, p, err := containerTarget(ctx, id, raw)
	if err != nil {
		return nil, "", nil, err
	}
	if p == "/" {
		root.Close()
		return nil, "", nil, containerfs.ErrBadName
	}
	end := func() {}
	if of.project != "" {
		if end, err = service.Begin(of.project, "files"); err != nil {
			root.Close()
			return nil, "", nil, err
		}
	}

	return root, p, func() { end(); root.Close() }, nil
}

// DeleteContainerFile deletes a file, a link or a folder of a running container.
func (a *AppManagement) DeleteContainerFile(ctx echo.Context, id codegen.ContainerID, params codegen.DeleteContainerFileParams) error {
	root, p, done, err := containerChange(ctx, string(id), params.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer done()
	if err := root.Remove(p); err != nil {
		return containerFilesFailure(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr("OK")})
}

// MakeContainerFolder makes a folder in a running container.
func (a *AppManagement) MakeContainerFolder(ctx echo.Context, id codegen.ContainerID) error {
	var body codegen.ContainerFolderMake
	if err := ctx.Bind(&body); err != nil {
		return containerFilesFailure(ctx, errBadContainerPath{err})
	}
	root, p, done, err := containerChange(ctx, string(id), body.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer done()
	if err := root.Mkdir(p); err != nil {
		return containerFilesFailure(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr("OK")})
}

// WriteContainerFileContent writes a text file of a running container.
func (a *AppManagement) WriteContainerFileContent(ctx echo.Context, id codegen.ContainerID) error {
	var body codegen.ContainerFileWrite
	if err := ctx.Bind(&body); err != nil {
		return containerFilesFailure(ctx, errBadContainerPath{err})
	}
	root, p, done, err := containerChange(ctx, string(id), body.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer done()
	if err := root.WriteFile(p, strings.NewReader(body.Content), body.Replace != nil && *body.Replace); err != nil {
		return containerFilesFailure(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr("OK")})
}

// UploadContainerFile streams the one file of a multipart body into a running container.
func (a *AppManagement) UploadContainerFile(ctx echo.Context, id codegen.ContainerID, params codegen.UploadContainerFileParams) error {
	root, p, done, err := containerChange(ctx, string(id), params.Path)
	if err != nil {
		return containerFilesFailure(ctx, err)
	}
	defer done()
	reader, err := ctx.Request().MultipartReader()
	if err != nil {
		return containerFilesFailure(ctx, errBadContainerPath{err})
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			return containerFilesFailure(ctx, errBadContainerPath{errors.New("no part named file in the body")})
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		err = root.WriteFile(p, part, params.Replace != nil && *params.Replace)
		part.Close()
		if err != nil {
			return containerFilesFailure(ctx, err)
		}

		return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr("OK")})
	}
}

var errNotOwner = errors.New("only the owner can do this")

type errBadContainerPath struct{ error }

func containerFilesFailure(ctx echo.Context, err error) error {
	message := err.Error()
	var bad errBadContainerPath
	switch {
	case errors.Is(err, errNotOwner):
		return forbidden(ctx)
	case errors.Is(err, syscall.EROFS):
		message = "the container's files are read-only: " + message
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	case errors.As(err, &bad), errors.Is(err, containerfs.ErrNotAFile), errors.Is(err, containerfs.ErrPseudoFile),
		errors.Is(err, containerfs.ErrBadName), errors.Is(err, containerfs.ErrOtherFilesystem), errors.Is(err, syscall.EBUSY),
		errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENXIO), errors.Is(err, syscall.ENODEV):
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	case errors.Is(err, containerfs.ErrExist), errors.As(err, new(service.ErrAppBusy)):
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		message = "the box itself may not read this path: " + message
		return ctx.JSON(http.StatusForbidden, codegen.ResponseForbidden{Message: &message})
	case errors.Is(err, errContainerStopped):
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
	case errdefs.IsNotFound(err), errors.Is(err, os.ErrNotExist):
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	case errors.Is(err, containerfs.ErrUnsupported):
		return ctx.JSON(http.StatusNotImplemented, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
}

// mountOf is the mount point dir is in, the deepest one, or "".
func mountOf(mounts []string, dir string) string {
	best := ""
	for _, m := range mounts {
		if (dir == m || m == "/" || strings.HasPrefix(dir, m+"/")) && len(m) > len(best) {
			best = m
		}
	}

	return best
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}

	return false
}

// isText is valid UTF-8 with no NUL byte.
func isText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// modeText is the permissions as `ls -l` writes them, setuid, setgid and sticky included.
func modeText(m fs.FileMode) string {
	text := []byte(m.Perm().String()[1:])
	special := func(at int, on bool, lower, upper byte) {
		if on {
			if text[at] == 'x' {
				text[at] = lower
			} else {
				text[at] = upper
			}
		}
	}
	special(2, m&fs.ModeSetuid != 0, 's', 'S')
	special(5, m&fs.ModeSetgid != 0, 's', 'S')
	special(8, m&fs.ModeSticky != 0, 't', 'T')

	return string(text)
}

// modeOctal is the permissions in octal, `1777` for /tmp.
func modeOctal(m fs.FileMode) string {
	bits := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		bits |= 0o1000
	}

	return fmt.Sprintf("%o", bits)
}
