//go:build linux

package containerfs

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

// a container root: /app/config.yml, /app/data/, /etc/passwd, and links that try to leave
func fakeRoot(t *testing.T) (string, *Root) {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "app", "data"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "etc"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app", "config.yml"), []byte("port: 8080\n"), 0o640))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "etc", "passwd"), []byte("root:x:0:0::/root:/bin/sh\nnode:x:1000:1000::/home/node:/bin/sh\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "etc", "group"), []byte("root:x:0:\nnode:x:1000:\n"), 0o644))
	// absolute: inside the container it means the container's /etc
	assert.NilError(t, os.Symlink("/etc", filepath.Join(dir, "app", "etc-link")))
	assert.NilError(t, os.Symlink("../../../../../../../etc/hostname", filepath.Join(dir, "app", "climb")))

	root, err := Open(dir)
	if err == ErrUnsupported {
		t.Skip(err)
	}
	assert.NilError(t, err)
	t.Cleanup(func() { root.Close() })

	return dir, root
}

func TestAFolderIsListedFoldersFirstWithItsModes(t *testing.T) {
	_, root := fakeRoot(t)

	entries, truncated, err := root.List("/app", 100)
	assert.NilError(t, err)
	assert.Assert(t, !truncated)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.DeepEqual(t, names, []string{"data", "climb", "config.yml", "etc-link"})
	assert.Assert(t, entries[0].IsDir)
	assert.Equal(t, entries[2].Mode.String(), "-rw-r-----")
	assert.Equal(t, entries[2].Size, int64(11))
	assert.Assert(t, entries[3].IsLink)
	assert.Equal(t, entries[3].Link, "/etc")
}

func TestALinkResolvesInsideTheContainer(t *testing.T) {
	_, root := fakeRoot(t)

	// /app/etc-link points at /etc: the container's, which holds passwd and group only
	entries, _, err := root.List("/app/etc-link", 100)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 2)

	// `..` beyond the root stops at the root: the host's /etc/hostname is not reached
	_, _, err = root.OpenFile("/app/climb")
	assert.Assert(t, os.IsNotExist(err), err)
	_, _, err = root.OpenFile("/../../etc/hostname")
	assert.Assert(t, os.IsNotExist(err), err)
}

func TestAFileIsReadAndAFolderIsNotAFile(t *testing.T) {
	_, root := fakeRoot(t)

	f, info, err := root.OpenFile("/app/config.yml")
	assert.NilError(t, err)
	data, _ := io.ReadAll(f)
	f.Close()
	assert.Equal(t, string(data), "port: 8080\n")
	assert.Equal(t, info.Size(), int64(11))

	_, _, err = root.OpenFile("/app/data")
	assert.Equal(t, err, ErrNotAFile)
}

func TestAListIsCutAtItsMaximum(t *testing.T) {
	_, root := fakeRoot(t)

	entries, truncated, err := root.List("/app", 2)
	assert.NilError(t, err)
	assert.Assert(t, truncated)
	assert.Equal(t, len(entries), 2)
}

func TestNamesAreReadFromTheContainer(t *testing.T) {
	_, root := fakeRoot(t)

	assert.Equal(t, root.Names("/etc/passwd")[1000], "node")
	assert.Equal(t, root.Names("/etc/group")[0], "root")
	assert.Equal(t, len(root.Names("/etc/shadow")), 0)
}

func TestAPathMustBeAbsolute(t *testing.T) {
	_, err := Clean("app")
	assert.Assert(t, err != nil)
	p, err := Clean("/app/../etc/")
	assert.NilError(t, err)
	assert.Equal(t, p, "/etc")
}
