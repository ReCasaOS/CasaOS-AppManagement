package service

import (
	"context"
	"sync"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/docker/api/types"
	client2 "github.com/docker/docker/client"
	"go.uber.org/zap"
)

// SampleContainerStats reads what each of these containers is using, right now.
//
// Nothing here touches the older GetContainerStats and its package-level map:
// that one spins a hundred-iteration background loop into a shared sync.Map and
// then busy-waits on it, which is fine for the single v1 caller it was written
// for and wrong for a request that wants one app's numbers and wants them now.
//
// Sampling is concurrent because each container costs about a second of waiting
// on the daemon, and an app's containers are few: serially, a five-service stack
// would keep the caller for five seconds to learn what it could learn in one.
// One client is shared -- it is safe across goroutines, and one connection per
// container per refresh is a cost with nothing to show for it.
//
// Containers the daemon cannot answer for are absent from the result rather than
// present with zeroes: "not measured" and "using nothing" are different answers,
// and zero is a lie the caller cannot see through.
func (ds *dockerService) SampleContainerStats(ctx context.Context, ids []string) map[string]*types.StatsJSON {
	out := map[string]*types.StatsJSON{}
	if len(ids) == 0 {
		return out
	}

	cli, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to open a docker client for stats", zap.Error(err))
		return out
	}
	defer cli.Close()

	var (
		lock sync.Mutex
		wg   sync.WaitGroup
	)

	for _, id := range ids {
		wg.Add(1)

		go func(id string) {
			defer wg.Done()

			stats, err := docker.SampleContainerStats(ctx, cli, id)
			if err != nil {
				// A container that stopped while being sampled is the common case
				// here, and it is not worth an error level.
				logger.Info("could not sample container stats", zap.Error(err), zap.String("container", id))
				return
			}

			lock.Lock()
			defer lock.Unlock()
			out[id] = stats
		}(id)
	}

	wg.Wait()

	return out
}

// VolumeMountpoints asks the daemon where each of these volumes lives.
//
// A volume it cannot place is absent from the result rather than present with a
// guessed path: the plan skips those and says so, which is the difference between
// a backup that reports what it could not take and one that reports success
// having copied an empty folder.
func (ds *dockerService) VolumeMountpoints(ctx context.Context, names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}

	cli, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to open a docker client for volumes", zap.Error(err))
		return out
	}
	defer cli.Close()

	for _, name := range names {
		mountpoint, err := docker.VolumeMountpoint(ctx, cli, name)
		if err != nil {
			logger.Info("could not locate volume", zap.Error(err), zap.String("volume", name))
			continue
		}

		out[name] = mountpoint
	}

	return out
}
