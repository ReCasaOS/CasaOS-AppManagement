//go:build linux

// Package containerfs reads the files of a running container from the host.
//
// A container's own filesystem, its image layers and its volumes as the container sees
// them, is /proc/<pid>/root on the host. Every path is resolved below that root with
// openat2(RESOLVE_IN_ROOT): `..` stops at the container's `/`, and a symlink, even an
// absolute one, resolves inside the container as it would for the app. So this works for
// images without a shell or `ls`, and gives the real owner and mode of every file.
package containerfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Entry is one name in a directory.
type Entry struct {
	Name     string
	IsDir    bool
	IsLink   bool
	Link     string // where a symlink points, as written
	Size     int64
	Mode     fs.FileMode // the permission bits, setuid, setgid and sticky included
	UID, GID uint32
	Modified time.Time
}

// Root is a container's filesystem, held open.
type Root struct{ fd int }

// ErrUnsupported is the kernel saying it has no openat2 (before Linux 5.6).
var ErrUnsupported = errors.New("this kernel cannot read a container's files safely (Linux 5.6 or newer is needed)")

// OfProcess opens the filesystem of the container whose main process is pid.
func OfProcess(pid int) (*Root, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("no process")
	}

	return Open(fmt.Sprintf("/proc/%d/root", pid))
}

// Open opens dir as the root every path is resolved below.
func Open(dir string) (*Root, error) {
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}

	return &Root{fd: fd}, nil
}

// Close lets the root go.
func (r *Root) Close() error { return unix.Close(r.fd) }

// Clean is p as an absolute path inside the container, or an error when it is not one.
func Clean(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("a path inside the container starts with /")
	}

	return path.Clean(p), nil
}

func (r *Root) open(p string, flags int) (*os.File, error) {
	rel := strings.TrimPrefix(p, "/")
	if rel == "" {
		rel = "."
	}
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	}
	var fd int
	var err error
	// the kernel answers EAGAIN when a rename or a mount raced the resolution
	for try := 0; try < 3; try++ {
		if fd, err = unix.Openat2(r.fd, rel, &how); err != unix.EAGAIN {
			break
		}
	}
	if errors.Is(err, unix.ENOSYS) {
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: err}
	}

	return os.NewFile(uintptr(fd), p), nil
}

// readMax bounds the names read from one folder before the cut to the max shown.
const readMax = 100000

// List is what dir holds, folders first, then by name, at most max entries; truncated
// says more were left out.
func (r *Root) List(dir string, max int) (entries []Entry, truncated bool, err error) {
	f, err := r.open(dir, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	// read further than shown, so that the cut keeps the first names in order rather
	// than whichever the disk gave first
	names, err := f.Readdirnames(readMax)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	sort.Strings(names)
	if len(names) > max {
		names, truncated = names[:max], true
	}
	fd := int(f.Fd())
	for _, name := range names {
		var st unix.Stat_t
		// shortcut: a dead network mount below dir hangs this stat, and the request with it,
		// as it hangs `ls`; a timeout per entry if it is ever met
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // gone since it was listed
		}
		entry := Entry{
			Name:     name,
			Size:     st.Size,
			Mode:     Permissions(st.Mode),
			UID:      st.Uid,
			GID:      st.Gid,
			Modified: time.Unix(st.Mtim.Unix()),
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			entry.IsDir, entry.Size = true, 0
		case unix.S_IFLNK:
			entry.IsLink = true
			buf := make([]byte, 4096)
			if n, err := unix.Readlinkat(fd, name, buf); err == nil {
				entry.Link = string(buf[:n])
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, truncated, nil
}

// ErrNotAFile is a path that is a folder, a device or a socket rather than a file.
var ErrNotAFile = errors.New("not a file")

// ErrPseudoFile is a file of /proc, /sys and the like: made up by the kernel when read,
// with the rights of the host's root rather than the container's.
var ErrPseudoFile = errors.New("a file of the kernel (/proc, /sys) is not read from here")

// pseudo are the filesystems whose files the kernel makes up when read.
var pseudo = map[int64]bool{
	unix.PROC_SUPER_MAGIC: true, unix.SYSFS_MAGIC: true, unix.DEBUGFS_MAGIC: true,
	unix.SECURITYFS_MAGIC: true, unix.CGROUP_SUPER_MAGIC: true, unix.CGROUP2_SUPER_MAGIC: true,
	unix.TRACEFS_MAGIC: true, unix.BPF_FS_MAGIC: true, unix.EFIVARFS_MAGIC: true,
}

// OpenFile opens a regular file for reading. The caller closes it.
//
// The path is first opened O_PATH, which reaches no driver: a device node, even one an
// image made in place of /etc/passwd, is never opened by the host's root. Only a regular
// file of a real filesystem is then opened for reading, through that very inode.
func (r *Root) OpenFile(p string) (*os.File, fs.FileInfo, error) {
	ref, err := r.open(p, unix.O_PATH)
	if err != nil {
		return nil, nil, err
	}
	defer ref.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(ref.Fd()), &st); err != nil {
		return nil, nil, &os.PathError{Op: "stat", Path: p, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, nil, ErrNotAFile
	}
	var sfs unix.Statfs_t
	if err := unix.Fstatfs(int(ref.Fd()), &sfs); err != nil {
		return nil, nil, &os.PathError{Op: "statfs", Path: p, Err: err}
	}
	if pseudo[int64(sfs.Type)] {
		return nil, nil, ErrPseudoFile
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", ref.Fd()), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), p)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}

	return f, info, nil
}

// Names reads an /etc/passwd or /etc/group file of the container: id to name. Empty
// when there is none, as in an image built from scratch.
func (r *Root) Names(file string) map[uint32]string {
	out := map[uint32]string{}
	f, _, err := r.OpenFile(file)
	if err != nil {
		return out
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 3 || fields[0] == "" || strings.HasPrefix(fields[0], "#") {
			continue
		}
		var id uint32
		if _, err := fmt.Sscan(fields[2], &id); err == nil {
			if _, seen := out[id]; !seen {
				out[id] = fields[0]
			}
		}
	}

	return out
}

// Permissions is the mode bits of a stat as a FileMode: rwx, setuid, setgid and sticky.
func Permissions(mode uint32) fs.FileMode {
	perm := fs.FileMode(mode & 0o777)
	if mode&unix.S_ISUID != 0 {
		perm |= fs.ModeSetuid
	}
	if mode&unix.S_ISGID != 0 {
		perm |= fs.ModeSetgid
	}
	if mode&unix.S_ISVTX != 0 {
		perm |= fs.ModeSticky
	}

	return perm
}
