package rclone

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
)

// Opt-in against the release's real daemon, with no remote of its own: every path is a
// local folder under t.TempDir().
func TestSyncWithRealRclone(t *testing.T) {
	if os.Getenv("CASAOS_RCLONE_INTEGRATION") != "1" {
		t.Skip("set CASAOS_RCLONE_INTEGRATION=1 to test a local rclone v1.75.1 daemon")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the production daemon uses a Unix socket")
	}
	binary := os.Getenv("RCLONE_BINARY")
	if binary == "" {
		binary = "rclone"
	}
	root := t.TempDir()
	config := filepath.Join(root, "rclone.conf")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "rclone.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "rcd", "--rc-addr", "unix://"+socket, "--rc-no-auth", "--config", config, "--log-level", "ERROR")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &Client{http: resty.New().SetTransport(transport).SetBaseURL("http://localhost").SetTimeout(10 * time.Second)}
	for {
		if err := client.callContext(ctx, "/rc/noop", nil, nil); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the isolated rclone daemon did not become ready")
		case <-time.After(20 * time.Millisecond):
		}
	}
	fastPolling(t)

	folder := func(name string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		return dir
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir, name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}

		return string(data)
	}
	// bisync compares modtime and size: a later edit must land on a later second
	later := func() { time.Sleep(1100 * time.Millisecond) }

	t.Run("copy keeps the overwritten version", func(t *testing.T) {
		src, dst, archive := folder("src"), folder("dst"), folder("archive")
		write(dst, "f1", "old")
		write(src, "f1", "newer-content")
		if err := client.SyncDirectory(ctx, src, dst, SyncOptions{BackupDir: archive, MaxDelete: -1}); err != nil {
			t.Fatal(err)
		}
		if read(dst, "f1") != "newer-content" || read(archive, "f1") != "old" {
			t.Fatalf("dst %q, archive %q", read(dst, "f1"), read(archive, "f1"))
		}
	})

	t.Run("mirror archives what it deletes", func(t *testing.T) {
		src, dst, archive := folder("src"), folder("dst"), folder("archive")
		write(src, "keep", "k")
		write(dst, "keep", "k")
		write(dst, "gone", "bye")
		if err := client.SyncDirectory(ctx, src, dst, SyncOptions{Mirror: true, BackupDir: archive, MaxDelete: -1}); err != nil {
			t.Fatal(err)
		}
		if read(dst, "gone") != "<missing>" || read(archive, "gone") != "bye" {
			t.Fatalf("dst %q, archive %q", read(dst, "gone"), read(archive, "gone"))
		}
	})

	t.Run("the cap stops a mirror", func(t *testing.T) {
		src, dst := folder("src"), folder("dst")
		for i := range 10 {
			write(dst, "f"+string(rune('0'+i)), "x")
		}
		write(src, "f0", "x")
		write(src, "f1", "x")
		err := client.SyncDirectory(ctx, src, dst, SyncOptions{Mirror: true, MaxDelete: 3})
		if !errors.Is(err, ErrTooManyDeletes) {
			t.Fatalf("got %v", err)
		}
		left, _ := os.ReadDir(dst)
		if len(left) < 7 {
			t.Fatalf("%d files left", len(left))
		}
	})

	// a pair of folders already synced once, with a few untouched files around
	pair := func() (p1, p2, workdir, archive2 string) {
		p1, p2, workdir, archive2 = folder("p1"), folder("p2"), folder("work"), folder("archive2")
		for _, name := range []string{"a", "b", "c"} {
			write(p1, name, "same "+name)
		}

		return
	}

	t.Run("both: first pass unites", func(t *testing.T) {
		p1, p2, workdir, _ := pair()
		write(p1, "x", "from 1")
		write(p2, "y", "from 2")
		if err := client.BisyncDirectory(ctx, p1, p2, BisyncOptions{Workdir: workdir, Resync: true}); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{p1, p2} {
			if read(dir, "x") != "from 1" || read(dir, "y") != "from 2" {
				t.Fatalf("%s: x %q y %q", dir, read(dir, "x"), read(dir, "y"))
			}
		}
	})

	t.Run("both: a deletion crosses and is archived", func(t *testing.T) {
		p1, p2, workdir, archive2 := pair()
		write(p1, "x", "from 1")
		o := BisyncOptions{Workdir: workdir, BackupDir2: archive2}
		if err := client.BisyncDirectory(ctx, p1, p2, BisyncOptions{Workdir: workdir, Resync: true}); err != nil {
			t.Fatal(err)
		}
		later()
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatal(err)
		}
		later()
		if err := os.Remove(filepath.Join(p1, "x")); err != nil {
			t.Fatal(err)
		}
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatal(err)
		}
		if read(p2, "x") != "<missing>" || read(archive2, "x") != "from 1" {
			t.Fatalf("p2 %q, archive %q", read(p2, "x"), read(archive2, "x"))
		}
	})

	t.Run("both: a conflict keeps both, extension last", func(t *testing.T) {
		p1, p2, workdir, _ := pair()
		write(p1, "photo.jpg", "original")
		o := BisyncOptions{Workdir: workdir}
		if err := client.BisyncDirectory(ctx, p1, p2, BisyncOptions{Workdir: workdir, Resync: true}); err != nil {
			t.Fatal(err)
		}
		later()
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatal(err)
		}
		later()
		write(p1, "photo.jpg", "edited on one")
		write(p2, "photo.jpg", "edited on two, longer")
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{p1, p2} {
			if read(dir, "photo.conflict1.jpg") == "<missing>" || read(dir, "photo.conflict2.jpg") == "<missing>" {
				left, _ := os.ReadDir(dir)
				t.Fatalf("%s: conflicts missing among %v", dir, left)
			}
		}
	})

	t.Run("both: more than half deleted is refused", func(t *testing.T) {
		p1, p2, workdir, _ := pair()
		write(p1, "d", "same d") // an emptied side is refused for another reason: keep one file
		o := BisyncOptions{Workdir: workdir}
		if err := client.BisyncDirectory(ctx, p1, p2, BisyncOptions{Workdir: workdir, Resync: true}); err != nil {
			t.Fatal(err)
		}
		later()
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatal(err)
		}
		later()
		for _, name := range []string{"a", "b", "c"} {
			if err := os.Remove(filepath.Join(p1, name)); err != nil {
				t.Fatal(err)
			}
		}
		if err := client.BisyncDirectory(ctx, p1, p2, o); !errors.Is(err, ErrTooManyDeletes) {
			t.Fatalf("got %v", err)
		}
		if read(p2, "a") != "same a" {
			t.Fatal("p2 was touched by a refused pass")
		}
		o.Force = true
		if err := client.BisyncDirectory(ctx, p1, p2, o); err != nil {
			t.Fatalf("forced pass: %v", err)
		}
		if read(p2, "a") != "<missing>" {
			t.Fatal("the forced pass did not delete")
		}
	})
}
