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
