//go:build linux

package containerfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// Writing into a container. Every change goes through the folder that holds the name,
// opened below the root as a read is: the name is then one component, created or removed
// relative to that folder, never followed when it is a link. What is created belongs to
// the owner of its folder, as the app would have made it.
//
// A mount is found by asking, not by comparing devices: two folders of one disk bound into
// a container share a device, and the kernel lets the host's root rename or remove a mount
// point of another namespace, detaching it there. So no name that is a mount point is
// ever renamed over, unlinked or emptied.

// ErrExist is a name already taken.
var ErrExist = errors.New("there is already something of that name")

// ErrBadName is a name that cannot be one: empty, `.`, `..` or the root.
var ErrBadName = errors.New("not a name a file can have")

// ErrOtherFilesystem is a mount point, or a folder that holds one.
var ErrOtherFilesystem = errors.New("a volume or a folder of the box is mounted here: it is not deleted from here")

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

// openBelow opens the one name of dir without following a link or crossing into a mount:
// a mount point answers EXDEV.
func openBelow(dir int, name string, flags int) (int, error) {
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_NOFOLLOW | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_NO_XDEV | unix.RESOLVE_NO_MAGICLINKS,
	}
	for try := 0; ; try++ {
		fd, err := unix.Openat2(dir, name, &how)
		if err != unix.EAGAIN || try == 2 {
			return fd, err
		}
	}
}

// isMount says whether the name of dir is a mount point of the tree it is reached
// through: the container's own mounts, whichever namespace made them.
//
// shortcut: a mount point of another container reached through this one's own folders
// (a folder of the box bound into two apps) is not seen, and removing it detaches it
// there until that app restarts, as `rm -rf` from the host's shell would; reading every
// container's mountinfo would see it.
func isMount(dir int, name string) (bool, error) {
	fd, err := openBelow(dir, name, unix.O_PATH)
	if errors.Is(err, unix.EXDEV) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	unix.Close(fd)

	return false, nil
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
	made, err := openBelow(fd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(made)

	return unix.Fchown(made, uid, gid)
}

// WriteFile writes content to p. An existing file is replaced only when replace is set,
// keeping its mode and owner; a new one is 0644, owned as its folder. A name that is a
// folder, a link or anything but a file is refused. The file is written aside and moved
// in place, so a failure leaves the old one whole. A file the container has bound in
// from the box is written in place instead, so the box's file is the one changed.
func (r *Root) WriteFile(p string, content io.Reader, replace bool) error {
	dir, name, err := r.parent(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd := int(dir.Fd())

	mode, uid, gid := uint32(0o644), 0, 0
	mounted := false
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
		if mounted, err = isMount(fd, name); err != nil {
			return &os.PathError{Op: "open", Path: p, Err: err}
		}
	case errors.Is(err, unix.ENOENT):
		if uid, gid, err = owner(dir); err != nil {
			return err
		}
	default:
		return &os.PathError{Op: "stat", Path: p, Err: err}
	}

	tmp, f, err := createAside(fd)
	if err != nil {
		return &os.PathError{Op: "create", Path: p, Err: err}
	}
	defer f.Close()
	moved := false
	defer func() {
		if !moved {
			unix.Unlinkat(fd, tmp, 0)
		}
	}()
	if _, err := io.Copy(f, content); err != nil {
		return err
	}
	if mounted {
		// shortcut: the content is held aside in the container's own folder first, so a
		// container whose own files are read-only cannot have a file of the box rewritten
		// here; an O_TMPFILE on the box's disk if this is ever met
		return copyInto(fd, name, f, p)
	}
	// chown first: it clears setuid and setgid, which the chmod then puts back
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := unix.Fchmod(int(f.Fd()), mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if replace {
		err = unix.Renameat(fd, tmp, fd, name)
	} else {
		err = placeNew(fd, tmp, name)
	}
	if errors.Is(err, unix.EEXIST) {
		return ErrExist // made by the app in the meantime: left as it is
	}
	if err != nil {
		return &os.PathError{Op: "write", Path: p, Err: err}
	}
	moved = true

	return nil
}

// placeNew gives tmp the name, failing when the name was taken since it was looked at.
func placeNew(dir int, tmp, name string) error {
	err := unix.Renameat2(dir, tmp, dir, name, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	// a filesystem without RENAME_NOREPLACE: a link fails the same way, then the aside
	// name goes
	if err := unix.Linkat(dir, tmp, dir, name, 0); err != nil {
		return err
	}
	unix.Unlinkat(dir, tmp, 0) // the file is in place: an aside name left behind is harmless

	return nil
}

// createAside makes a new file in dir, for a content before it takes its name.
func createAside(dir int) (string, *os.File, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	tmp := ".recasaos-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dir, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", nil, err
	}

	return tmp, os.NewFile(uintptr(fd), tmp), nil
}

// copyInto writes what from holds over the regular file name of dir, as OpenFile reads
// one: opened O_PATH, checked, then reopened for writing through that inode.
func copyInto(dir int, name string, from *os.File, p string) error {
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_MAGICLINKS}
	ref, err := unix.Openat2(dir, name, &how)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(ref)
	var st unix.Stat_t
	if err := unix.Fstat(ref, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrNotAFile
	}
	var sfs unix.Statfs_t
	if err := unix.Fstatfs(ref, &sfs); err != nil {
		return err
	}
	if pseudo[int64(sfs.Type)] {
		return ErrPseudoFile
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", ref), unix.O_WRONLY|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	to := os.NewFile(uintptr(fd), name)
	defer to.Close()
	size, err := from.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := from.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// the room is taken before the file is cut: a full disk fails here, the file whole
	if size > 0 {
		if err := unix.Fallocate(fd, 0, 0, size); err != nil && !errors.Is(err, unix.EOPNOTSUPP) {
			return &os.PathError{Op: "write", Path: p, Err: err}
		}
	}
	if err := to.Truncate(0); err != nil {
		return err
	}
	if _, err := io.Copy(to, from); err != nil {
		return &os.PathError{Op: "write", Path: p, Err: err}
	}

	return to.Sync()
}

// Remove deletes p: a file or a link itself, or a folder and all it holds. A mount point,
// or a folder that holds one, is refused before anything goes.
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
	if mounted, err := isMount(fd, name); err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	} else if mounted {
		return ErrOtherFilesystem
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err := unix.Unlinkat(fd, name, 0); err != nil {
			return &os.PathError{Op: "remove", Path: p, Err: err}
		}
		return nil
	}
	if err := walk(fd, name, p, false); err != nil {
		return err
	}

	return walk(fd, name, p, true)
}

// walk goes through the folder name of dir, whose own name is no mount. It refuses a mount
// point anywhere in it; with remove, it deletes as it goes, the folder last. It is run once
// without remove, so that nothing goes when a mount is found, and checks again while it
// removes, for what was mounted in between.
func walk(dir int, name, p string, remove bool) error {
	fd, err := openBelow(dir, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if errors.Is(err, unix.EXDEV) {
		return ErrOtherFilesystem
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return &os.PathError{Op: "read", Path: p, Err: err}
	}
	for _, child := range names {
		at := path.Join(p, child)
		var st unix.Stat_t
		if err := unix.Fstatat(fd, child, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // gone since it was listed
			}
			return &os.PathError{Op: "stat", Path: at, Err: err}
		}
		if mounted, err := isMount(fd, child); err != nil {
			return &os.PathError{Op: "open", Path: at, Err: err}
		} else if mounted {
			return ErrOtherFilesystem
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := walk(fd, child, at, remove); err != nil {
				return err
			}
		} else if remove {
			if err := unix.Unlinkat(fd, child, 0); err != nil {
				return &os.PathError{Op: "remove", Path: at, Err: err}
			}
		}
	}
	if remove {
		if err := unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil {
			return &os.PathError{Op: "remove", Path: p, Err: err}
		}
	}

	return nil
}
