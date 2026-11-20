//go:build linux

package containerfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"gotest.tools/v3/assert"
)

func TestAFolderAndAFileAreMadeOwnedAsTheirFolder(t *testing.T) {
	dir, root := fakeRoot(t)
	if os.Geteuid() == 0 {
		assert.NilError(t, os.Chown(filepath.Join(dir, "app"), 1000, 1000))
	}

	assert.NilError(t, root.Mkdir("/app/new"))
	assert.Equal(t, root.Mkdir("/app/new"), ErrExist)
	assert.NilError(t, root.WriteFile("/app/new/a.txt", strings.NewReader("hello"), false))
	data, err := os.ReadFile(filepath.Join(dir, "app", "new", "a.txt"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "hello")

	if os.Geteuid() == 0 {
		var st unix.Stat_t
		assert.NilError(t, unix.Stat(filepath.Join(dir, "app", "new", "a.txt"), &st))
		assert.Equal(t, st.Uid, uint32(1000))
	}
	assert.Equal(t, root.Mkdir("/"), ErrBadName)
}

func TestAFileIsReplacedOnlyWhenAskedAndKeepsItsMode(t *testing.T) {
	dir, root := fakeRoot(t)

	assert.Equal(t, root.WriteFile("/app/config.yml", strings.NewReader("x"), false), ErrExist)
	assert.NilError(t, root.WriteFile("/app/config.yml", strings.NewReader("port: 9090\n"), true))
	info, err := os.Stat(filepath.Join(dir, "app", "config.yml"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o640))
	data, _ := os.ReadFile(filepath.Join(dir, "app", "config.yml"))
	assert.Equal(t, string(data), "port: 9090\n")

	// nothing is left aside
	names, _ := os.ReadDir(filepath.Join(dir, "app"))
	for _, n := range names {
		assert.Assert(t, !strings.Contains(n.Name(), ".recasaos-"), n.Name())
	}
}

// A link is never written through: a link to /etc/passwd stays a link, and the file it
// names is untouched.
func TestALinkIsNotWrittenThrough(t *testing.T) {
	dir, root := fakeRoot(t)

	assert.Equal(t, root.WriteFile("/app/etc-link", strings.NewReader("x"), true), ErrNotAFile)
	assert.Equal(t, root.WriteFile("/app/data", strings.NewReader("x"), true), ErrNotAFile)
	// the folder of the name is resolved inside the container: /app/etc-link/passwd is the
	// container's /etc/passwd, never the host's
	assert.NilError(t, root.WriteFile("/app/etc-link/passwd", strings.NewReader("root:x:0:0::/:/bin/sh\n"), true))
	data, _ := os.ReadFile(filepath.Join(dir, "etc", "passwd"))
	assert.Equal(t, string(data), "root:x:0:0::/:/bin/sh\n")
}

func TestAFolderIsRemovedWithWhatItHoldsAndALinkAlone(t *testing.T) {
	dir, root := fakeRoot(t)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app", "data", "x"), []byte("x"), 0o644))

	assert.NilError(t, root.Remove("/app/etc-link"))
	_, err := os.Stat(filepath.Join(dir, "etc", "passwd")) // the link went, not what it names
	assert.NilError(t, err)

	assert.NilError(t, root.Remove("/app/data"))
	_, err = os.Stat(filepath.Join(dir, "app", "data"))
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, root.Remove("/"), ErrBadName)
}

func TestAFolderHoldingAnotherFilesystemIsNotRemoved(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mount needs root")
	}
	dir, root := fakeRoot(t)
	mnt := filepath.Join(dir, "app", "data")
	assert.NilError(t, unix.Mount("tmpfs", mnt, "tmpfs", 0, ""))
	defer unix.Unmount(mnt, unix.MNT_DETACH)
	assert.NilError(t, os.WriteFile(filepath.Join(mnt, "kept"), []byte("x"), 0o644))

	assert.Equal(t, root.Remove("/app"), ErrOtherFilesystem)
	assert.Equal(t, root.Remove("/app/data"), ErrOtherFilesystem) // the mount point itself
	_, err := os.Stat(filepath.Join(mnt, "kept"))
	assert.NilError(t, err)
	_, err = os.Stat(filepath.Join(dir, "app", "config.yml")) // nothing went before the refusal
	assert.NilError(t, err)
}

func TestNothingIsWrittenIntoTheKernelsFiles(t *testing.T) {
	root, err := Open("/")
	if err == ErrUnsupported {
		t.Skip(err)
	}
	assert.NilError(t, err)
	defer root.Close()

	assert.Equal(t, root.WriteFile("/proc/sys/kernel/hostname", strings.NewReader("x"), true), ErrPseudoFile)
	assert.Equal(t, root.Mkdir("/sys/fs/x"), ErrPseudoFile)
}
