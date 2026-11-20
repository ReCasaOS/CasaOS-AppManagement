//go:build linux

package containerfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// Writing into a container. Every change goes through the folder that holds the name,
// opened below the root as a read is: the name is then one component, created or removed
// relative to that folder, never followed when it is a link. What is created belongs to
// the owner of its folder, as the app would have made it.

// ErrExist is a name already taken.
var ErrExist = errors.New("there is already something of that name")

// ErrBadName is a name that cannot be one: empty, `.`, `..` or the root.
var ErrBadName = errors.New("not a name a file can have")

// ErrOtherFilesystem is a folder that holds another filesystem, a volume mounted below it.
var ErrOtherFilesystem = errors.New("a volume or another filesystem is mounted inside this folder: delete inside it instead")

// parent opens the folder of p for changes in it, and answers the name in it.
func (r *Root) parent(p string) (*os.File, string, error) {
	dir, name := path.Split(p)
	if name == "" || name == "." || name == ".." {
		return nil, "", ErrBadName
	}
	f, err := r.open(dir, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, "", err
	}
	var sfs unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &sfs); err != nil {
		f.Close()
		return nil, "", &os.PathError{Op: "statfs", Path: dir, Err: err}
	}
	if pseudo[int64(sfs.Type)] {
		f.Close()
		return nil, "", ErrPseudoFile
	}

	return f, name, nil
}

func owner(dir *os.File) (uid, gid int, err error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return 0, 0, err
	}

	return int(st.Uid), int(st.Gid), nil
}

// Mkdir makes the folder p, owned as its parent.
func (r *Root) Mkdir(p string) error {
	dir, name, err := r.parent(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	uid, gid, err := owner(dir)
	if err != nil {
		return err
	}
	fd := int(dir.Fd())
	if err := unix.Mkdirat(fd, name, 0o755); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrExist
		}
		return &os.PathError{Op: "mkdir", Path: p, Err: err}
	}

	return unix.Fchownat(fd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
}

// WriteFile writes content to p. An existing file is replaced only when replace is set,
// keeping its mode and owner; a new one is 0644, owned as its folder. A name that is a
// folder, a link or anything but a file is refused. The file is written aside and moved
// in place, so a failure leaves the old one whole.
func (r *Root) WriteFile(p string, content io.Reader, replace bool) error {
	dir, name, err := r.parent(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())

	mode, uid, gid := uint32(0o644), 0, 0
	var st unix.Stat_t
	switch err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return ErrNotAFile
		}
		if !replace {
			return ErrExist
		}
		mode, uid, gid = st.Mode&0o7777, int(st.Uid), int(st.Gid)
	case errors.Is(err, unix.ENOENT):
		if uid, gid, err = owner(dir); err != nil {
			return err
		}
	default:
		return &os.PathError{Op: "stat", Path: p, Err: err}
	}

	tmp, f, err := createAside(fd, name)
	if err != nil {
		return &os.PathError{Op: "create", Path: p, Err: err}
	}
	written := false
	defer func() {
		if !written {
			unix.Unlinkat(fd, tmp, 0)
		}
	}()
	if _, err := io.Copy(f, content); err != nil {
		f.Close()
		return err
	}
	// chown first: it clears setuid and setgid, which the chmod then puts back
	if err := f.Chown(uid, gid); err != nil {
		f.Close()
		return err
	}
	if err := unix.Fchmod(int(f.Fd()), mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	err = unix.Renameat(fd, tmp, fd, name)
	if err == nil {
		written = true // the aside file is the file now
	} else if errors.Is(err, unix.EBUSY) {
		// the file is itself mounted (a file of the box bound in): written in place, and
		// the aside file goes
		err = copyInto(fd, name, f)
	}
	f.Close()
	if err != nil {
		return &os.PathError{Op: "write", Path: p, Err: err}
	}

	return nil
}

// createAside makes a new file beside name, for its content before it takes the name.
func createAside(dir int, name string) (string, *os.File, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	tmp := "." + name + ".recasaos-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dir, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", nil, err
	}

	return tmp, os.NewFile(uintptr(fd), tmp), nil
}

// copyInto writes what from holds over the regular file name of dir.
func copyInto(dir int, name string, from *os.File) error {
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	to := os.NewFile(uintptr(fd), name)
	defer to.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrNotAFile
	}
	if _, err := from.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := to.Truncate(0); err != nil {
		return err
	}
	if _, err := io.Copy(to, from); err != nil {
		return err
	}

	return to.Sync()
}

// Remove deletes p: a file or a link itself, or a folder and all it holds. A folder that
// holds another filesystem, a volume mounted below it, is refused before anything goes.
func (r *Root) Remove(p string) error {
	dir, name, err := r.parent(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())

	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "stat", Path: p, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err := unix.Unlinkat(fd, name, 0); err != nil {
			return &os.PathError{Op: "remove", Path: p, Err: err}
		}
		return nil
	}
	// a volume's own mount point: emptying it would delete what the volume keeps
	var parentStat unix.Stat_t
	if err := unix.Fstat(fd, &parentStat); err != nil {
		return err
	}
	if parentStat.Dev != st.Dev {
		return ErrOtherFilesystem
	}
	if err := sameFilesystem(fd, name, st.Dev); err != nil {
		return err
	}

	return removeTree(fd, name, st.Dev, p)
}

// sameFilesystem walks the folder name of dir and refuses when anything in it is on
// another device than dev.
func sameFilesystem(dir int, name string, dev uint64) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, child := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, child, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if st.Dev != dev {
			return ErrOtherFilesystem
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := sameFilesystem(fd, child, dev); err != nil {
				return err
			}
		}
	}

	return nil
}

func removeTree(dir int, name string, dev uint64, p string) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	names, err := f.Readdirnames(-1)
	if err != nil {
		f.Close()
		return err
	}
	for _, child := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, child, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if st.Dev != dev {
			f.Close()
			return ErrOtherFilesystem // mounted since it was walked
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			err = removeTree(fd, child, dev, path.Join(p, child))
		} else {
			err = unix.Unlinkat(fd, child, 0)
		}
		if err != nil {
			f.Close()
			return &os.PathError{Op: "remove", Path: path.Join(p, child), Err: err}
		}
	}
	f.Close()
	if err := unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil {
		return &os.PathError{Op: "remove", Path: p, Err: err}
	}

	return nil
}
