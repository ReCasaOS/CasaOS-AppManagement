package dockerstats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

// fakeAPI stands in for the Docker client: no daemon needed.
type fakeAPI struct {
	listOpts   container.ListOptions
	listCtx    context.Context
	inspectCtx context.Context
	closed     atomic.Int32
	inFlight   atomic.Int32
	peak       atomic.Int32
	// stats decides the answer per id; nil means a valid small StatsResponse.
	stats func(ctx context.Context, id string) (container.StatsResponseReader, error)
}

type ctxKey struct{}

func (f *fakeAPI) ContainerList(ctx context.Context, o container.ListOptions) ([]container.Summary, error) {
	f.listOpts, f.listCtx = o, ctx
	return []container.Summary{{ID: "a"}}, nil
}

func (f *fakeAPI) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	f.inspectCtx = ctx
	return container.InspectResponse{}, nil
}

func (f *fakeAPI) ContainerStatsOneShot(ctx context.Context, id string) (container.StatsResponseReader, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.stats != nil {
		return f.stats(ctx, id)
	}
	time.Sleep(5 * time.Millisecond)
	return body(`{"id":"` + id + `"}`), nil
}

func (f *fakeAPI) Close() error { f.closed.Add(1); return nil }

func body(s string) container.StatsResponseReader {
	return container.StatsResponseReader{Body: io.NopCloser(strings.NewReader(s))}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("c%d", i)
	}
	return out
}

func TestStatsNeverRunsMoreThanEightCallsAtOnce(t *testing.T) {
	f := &fakeAPI{}
	got := (&DockerSource{api: f}).Stats(context.Background(), ids(40))
	assert.Equal(t, len(got), 40)
	assert.Assert(t, f.peak.Load() <= 8, "peak %d", f.peak.Load())
	assert.Assert(t, f.peak.Load() > 1, "calls should overlap")
}

func TestAFailingContainerIsAbsentAndOthersAreKept(t *testing.T) {
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		if id == "bad" {
			return container.StatsResponseReader{}, errors.New("boom")
		}
		return body(`{"id":"` + id + `"}`), nil
	}}
	got := (&DockerSource{api: f}).Stats(context.Background(), []string{"a", "bad", "b"})
	assert.Equal(t, len(got), 2)
	_, bad := got["bad"]
	assert.Assert(t, !bad)
	assert.Equal(t, got["a"].ID, "a")
}

func TestASlowContainerDoesNotDelayTheOthers(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan string, 4)
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		if id == "slow" {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return container.StatsResponseReader{}, errors.New("slow failed")
		}
		finished <- id
		return body(`{}`), nil
	}}
	done := make(chan map[string]*container.StatsResponse)
	go func() { done <- (&DockerSource{api: f}).Stats(context.Background(), []string{"slow", "a", "b", "c"}) }()
	// the three fast ones finish while the slow one is still held
	for i := 0; i < 3; i++ {
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("fast containers were delayed by the slow one")
		}
	}
	close(release)
	got := <-done
	assert.Equal(t, len(got), 3)
}

func TestCancellationStopsThePollingPromptly(t *testing.T) {
	var started atomic.Int32
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		started.Add(1)
		<-ctx.Done()
		return container.StatsResponseReader{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan map[string]*container.StatsResponse)
	go func() { done <- (&DockerSource{api: f}).Stats(ctx, ids(100)) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		assert.Equal(t, len(got), 0)
		assert.Assert(t, started.Load() <= maxInFlight+1, "started %d after cancel", started.Load())
	case <-time.After(10 * time.Second):
		t.Fatal("Stats did not return after cancellation")
	}
}

func TestHostileAnswersFailOnlyTheirContainer(t *testing.T) {
	huge := `{"id":"x"}` + strings.Repeat(" ", maxStatsBody+10)
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		switch id {
		case "huge":
			return body(huge), nil
		case "junk":
			return body(`{not json`), nil
		}
		return body(`{"id":"ok"}`), nil
	}}
	got := (&DockerSource{api: f}).Stats(context.Background(), []string{"huge", "junk", "ok"})
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got["ok"].ID, "ok")
}

func TestTheBodyIsClosedAndTheReadIsBounded(t *testing.T) {
	r := &endlessReader{}
	f := &fakeAPI{stats: func(ctx context.Context, id string) (container.StatsResponseReader, error) {
		return container.StatsResponseReader{Body: r}, nil
	}}
	got := (&DockerSource{api: f}).Stats(context.Background(), []string{"x"})
	assert.Equal(t, len(got), 0)
	assert.Assert(t, r.closed.Load())
	assert.Assert(t, r.read.Load() < 4*maxStatsBody, "read %d", r.read.Load())
}

type endlessReader struct {
	read   atomic.Int64
	closed atomic.Bool
}

func (c *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	c.read.Add(int64(len(p)))
	return len(p), nil
}
func (c *endlessReader) Close() error { c.closed.Store(true); return nil }

func TestListAsksForAllContainersWithTheCallersContext(t *testing.T) {
	f := &fakeAPI{}
	s := &DockerSource{api: f}
	ctx := context.WithValue(context.Background(), ctxKey{}, 1)
	_, err := s.List(ctx)
	assert.NilError(t, err)
	assert.Assert(t, f.listOpts.All)
	assert.Equal(t, f.listCtx, ctx)
	_, err = s.Inspect(ctx, "a")
	assert.NilError(t, err)
	assert.Equal(t, f.inspectCtx, ctx)
}

func TestCloseIsIdempotentAndClosesTheClientOnce(t *testing.T) {
	f := &fakeAPI{}
	s := &DockerSource{api: f}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
	assert.Equal(t, f.closed.Load(), int32(1))
}

func TestNewDockerSourceBuildsOneClient(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	s, err := NewDockerSource()
	assert.NilError(t, err)
	assert.Assert(t, s.api != nil)
	assert.NilError(t, s.Close())
	assert.NilError(t, s.Close())
}
