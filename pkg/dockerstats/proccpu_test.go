package dockerstats

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

// dockerStackProc reports whether a process name belongs to what a stats call makes
// work: dockerd, containerd, and the containerd shims that read the cgroups. The
// kernel cuts comm to 15 characters, so every shim variant reads "containerd-shim".
func dockerStackProc(comm string) bool {
	return comm == "dockerd" || comm == "containerd" || strings.HasPrefix(comm, "containerd-shim")
}

// procCPUTicks sums utime+stime (fields 14 and 15 of /proc/<pid>/stat, in clock ticks)
// of the processes under root, a /proc-like tree, whose name satisfies match, and
// returns the sum per process name for the log. A process that vanishes mid-scan or
// has an unreadable stat is skipped.
func procCPUTicks(root string, match func(comm string) bool) (map[string]float64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "stat"))
		if err != nil {
			continue
		}
		s := string(raw)
		open, end := strings.Index(s, "("), strings.LastIndex(s, ")")
		if open < 0 || end < open {
			continue
		}
		comm := s[open+1 : end]
		if !match(comm) {
			continue
		}
		f := strings.Fields(s[end+1:]) // starts at field 3
		if len(f) < 13 {
			continue
		}
		u, err1 := strconv.ParseFloat(f[11], 64)
		k, err2 := strconv.ParseFloat(f[12], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[comm] += u + k
	}
	return out, nil
}

func writeProc(t *testing.T, root, pid, comm string, utime, stime int) {
	t.Helper()
	dir := filepath.Join(root, pid)
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	// fields 3..13 are filler, 14 and 15 are utime and stime
	line := pid + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 " + strconv.Itoa(utime) + " " + strconv.Itoa(stime) + " 0 0 20 0 1 0\n"
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o644))
}

func TestProcCPUTicksSumsTheDockerStackOnly(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "100", "dockerd", 10, 5)
	writeProc(t, root, "200", "containerd", 7, 3)
	writeProc(t, root, "301", "containerd-shim", 4, 1)
	writeProc(t, root, "302", "containerd-shim", 2, 2)
	writeProc(t, root, "400", "bash", 999, 999)
	writeProc(t, root, "500", "my (weird) name", 50, 50) // parentheses inside comm
	assert.NilError(t, os.MkdirAll(filepath.Join(root, "self"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(root, "600"), 0o755)) // vanished: no stat
	writeProc(t, root, "700", "dockerd", 0, 0)
	assert.NilError(t, os.WriteFile(filepath.Join(root, "700", "stat"), []byte("700 (dockerd) S 1"), 0o644)) // truncated

	got, err := procCPUTicks(root, dockerStackProc)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]float64{"dockerd": 15, "containerd": 10, "containerd-shim": 9})

	all, err := procCPUTicks(root, func(string) bool { return true })
	assert.NilError(t, err)
	assert.Equal(t, all["my (weird) name"], 100.0)

	_, err = procCPUTicks(filepath.Join(root, "absent"), dockerStackProc)
	assert.Assert(t, err != nil)
}

func TestDockerStackProcNames(t *testing.T) {
	for _, c := range []string{"dockerd", "containerd", "containerd-shim"} {
		assert.Assert(t, dockerStackProc(c), c)
	}
	for _, c := range []string{"docker", "containerd-ctr", "runc", "sleep", ""} {
		assert.Assert(t, !dockerStackProc(c), c)
	}
}

func TestAHungContainerIsCutOffByThePerCallDeadline(t *testing.T) {
	old := statsCallTimeout
	statsCallTimeout = 50 * time.Millisecond
	defer func() { statsCallTimeout = old }()
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		<-ctx.Done() // a hung container: only a deadline frees it
		return container.StatsResponseReader{}, ctx.Err()
	}}
	// the caller's context stays alive: only the per-call deadline can end this
	done := make(chan map[string]*container.StatsResponse, 1)
	go func() { done <- (&DockerSource{api: f}).Stats(context.Background(), ids(24)) }()
	select {
	case got := <-done:
		assert.Equal(t, len(got), 0)
	case <-time.After(10 * time.Second):
		t.Fatal("hung containers held the tick past the per-call deadline")
	}
}
