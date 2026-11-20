//go:build !linux

package containerfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Entry is one name in a directory.
type Entry struct {
	Name     string
	IsDir    bool
	IsLink   bool
	Link     string
	Size     int64
	Mode     fs.FileMode
	UID, GID uint32
	Modified time.Time
}

// Root is a container's filesystem; only Linux has one.
type Root struct{}

// ErrUnsupported is every call off Linux.
var ErrUnsupported = errors.New("a container's files can only be read on Linux")

// ErrNotAFile is a path that is not a regular file.
var ErrNotAFile = errors.New("not a file")

// ErrPseudoFile is a file of /proc, /sys and the like.
var ErrPseudoFile = errors.New("a file of the kernel (/proc, /sys) is not read from here")

func OfProcess(int) (*Root, error) { return nil, ErrUnsupported }
func Open(string) (*Root, error)   { return nil, ErrUnsupported }
func (r *Root) Close() error       { return nil }

// Clean is p as an absolute path inside the container, or an error when it is not one.
func Clean(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", errors.New("a path inside the container starts with /")
	}

	return path.Clean(p), nil
}

func (r *Root) List(string, int) ([]Entry, bool, error) { return nil, false, ErrUnsupported }
func (r *Root) OpenFile(string) (*os.File, fs.FileInfo, error) {
	return nil, nil, ErrUnsupported
}
func (r *Root) Names(string) map[uint32]string { return map[uint32]string{} }

// ErrExist is a name already taken.
var ErrExist = errors.New("there is already something of that name")

// ErrBadName is a name that cannot be one.
var ErrBadName = errors.New("not a name a file can have")

// ErrOtherFilesystem is a folder that holds another filesystem.
var ErrOtherFilesystem = errors.New("a volume or another filesystem is mounted inside this folder: delete inside it instead")

func (r *Root) Mkdir(string) error                     { return ErrUnsupported }
func (r *Root) WriteFile(string, io.Reader, bool) error { return ErrUnsupported }
func (r *Root) Remove(string) error                    { return ErrUnsupported }
