package dockerstats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	client2 "github.com/docker/docker/client"
)

// Sampling cost: NOT MEASURED YET (docs/stats-sampling-cost.md). Strategy A, bounded
// one-shot polling, is the default; per-container streams replace it only if polling
// measures over 0.5 % of one core with 30 containers.

const (
	// maxInFlight bounds the simultaneous stats calls, so a host with hundreds of
	// containers never opens a burst of connections.
	maxInFlight = 8
	// maxStatsBody bounds what is read of one stats answer; a real one is a few KB.
	maxStatsBody = 1 << 20
)

// statsCallTimeout bounds one container's call, so hung containers cannot hold the
// 8 slots until the caller's own deadline. A variable only so tests can shorten it.
var statsCallTimeout = 2 * time.Second

// Source is where the sampler reads the containers from.
type Source interface {
	List(ctx context.Context) ([]container.Summary, error)
	Inspect(ctx context.Context, id string) (container.InspectResponse, error)
	// Stats measures the given containers; an id that could not be measured is absent.
	Stats(ctx context.Context, ids []string) map[string]*container.StatsResponse
}

var _ Source = (*DockerSource)(nil)

// dockerAPI is the part of the Docker client the source uses.
type dockerAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error)
	ContainerStatsOneShot(ctx context.Context, id string) (container.StatsResponseReader, error)
	Close() error
}

// DockerSource reads through one client shared for the life of the process.
type DockerSource struct {
	api  dockerAPI
	once sync.Once
	err  error
}

func NewDockerSource() (*DockerSource, error) {
	c, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerSource{api: c}, nil
}

// Close releases the client; calling it again does nothing.
func (d *DockerSource) Close() error {
	d.once.Do(func() { d.err = d.api.Close() })
	return d.err
}

func (d *DockerSource) List(ctx context.Context) ([]container.Summary, error) {
	return d.api.ContainerList(ctx, container.ListOptions{All: true})
}

func (d *DockerSource) Inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	return d.api.ContainerInspect(ctx, id)
}

func (d *DockerSource) Stats(ctx context.Context, ids []string) map[string]*container.StatsResponse {
	out := make(map[string]*container.StatsResponse, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxInFlight)
loop:
	for _, id := range ids {
		select {
		case sem <- struct{}{}:
			if ctx.Err() != nil { // a free slot and a done context are both ready: select picks at random
				<-sem
				break loop
			}
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if s, err := d.one(ctx, id); err == nil {
				mu.Lock()
				out[id] = s
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

var errStatsTooBig = errors.New("stats answer too large")

func (d *DockerSource) one(ctx context.Context, id string) (*container.StatsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, statsCallTimeout)
	defer cancel()
	r, err := d.api.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxStatsBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxStatsBody {
		return nil, errStatsTooBig
	}
	var s container.StatsResponse
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
