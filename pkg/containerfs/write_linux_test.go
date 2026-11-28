//go:build linux

package containerfs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The mounts of a container are made in its own namespace. From the host's, the kernel
// lets root rename over or remove such a mount point, detaching it in the container, and
// two folders of one disk share a device: neither a rename nor a device check is safe.
// Here a process in a namespace of its own binds folders and a file of one disk, as Docker
// does, and its root is read as a container's.
func TestTheMountsOfAContainerAreNeverDetachedNorEmptied(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("needs unshare")
	}
	base := t.TempDir()
	host, inside := filepath.Join(base, "host"), filepath.Join(base, "root")
	for _, d := range []string{"host/vol/www/media", "host/media", "root/config"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(base, d), 0o755))
	}
	assert.NilError(t, os.WriteFile(filepath.Join(host, "media", "precious"), []byte("precious"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(host, "conf.yml"), []byte("ORIGINAL"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(inside, "app.yml"), nil, 0o644))
	ready := filepath.Join(base, "ready")
	script := "mount --bind " + host + "/vol " + inside + "/config && mount --bind " + host + "/media " + inside + "/config/www/media && mount --bind " + host + "/conf.yml " + inside + "/app.yml && touch " + ready + " && exec sleep 60"
	ns := exec.Command("unshare", "-m", "--propagation", "private", "sh", "-c", script)
	assert.NilError(t, ns.Start())
	defer func() { ns.Process.Kill(); ns.Wait() }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	root, err := OfProcess(ns.Process.Pid)
	assert.NilError(t, err)
	defer root.Close()
	mounts := func() string {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", ns.Process.Pid))
		return string(data)
	}

	// a file bound in is written in place: the box's file changes, the mount stays
	assert.NilError(t, root.WriteFile(inside+"/app.yml", strings.NewReader("EDITED"), true))
	data, _ := os.ReadFile(filepath.Join(host, "conf.yml"))
	assert.Equal(t, string(data), "EDITED")
	assert.Assert(t, strings.Contains(mounts(), " "+inside+"/app.yml "))

	// nothing that is or holds a mount is removed, on one disk as on another
	assert.Equal(t, root.Remove(inside+"/config/www"), ErrOtherFilesystem)
	assert.Equal(t, root.Remove(inside+"/config/www/media"), ErrOtherFilesystem)
	assert.Equal(t, root.Remove(inside+"/app.yml"), ErrOtherFilesystem)
	_, err = os.Stat(filepath.Join(host, "media", "precious"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(mounts(), " "+inside+"/config/www/media "))
	assert.Assert(t, strings.Contains(mounts(), " "+inside+"/app.yml "))
}

func TestANameTakenMeanwhileIsNotOverwritten(t *testing.T) {
	dir, root := fakeRoot(t)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app", "late"), []byte("app's"), 0o644))
	// placeNew is what a write without replace ends with, after its first look
	f, err := os.Open(filepath.Join(dir, "app"))
	assert.NilError(t, err)
	defer f.Close()
	tmp, aside, err := createAside(int(f.Fd()))
	assert.NilError(t, err)
	aside.Close()
	assert.ErrorIs(t, placeNew(int(f.Fd()), tmp, "late"), unix.EEXIST)
	data, _ := os.ReadFile(filepath.Join(dir, "app", "late"))
	assert.Equal(t, string(data), "app's")
	_ = root
}
