//go:build !linux

package containerfs

import (
	"errors"
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
