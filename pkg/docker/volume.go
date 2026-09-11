package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// VolumeMountpoint is where the daemon keeps a named volume on this host.
//
// Asked rather than worked out. The path looks predictable --
// /var/lib/docker/volumes/<name>/_data -- right up until the daemon has a
// data-root of its own, or the volume has a driver that puts it somewhere else
// entirely. A backup that copies from a path nobody confirmed can find an empty
// folder and report success.
//
// An empty answer means the daemon does not know this volume.
func VolumeMountpoint(ctx context.Context, cli *client.Client, name string) (string, error) {
	volume, err := cli.VolumeInspect(ctx, name)
	if err != nil {
		return "", err
	}

	return volume.Mountpoint, nil
}

// VolumeUsage is how much room each named volume takes and how many containers
// reference it.
//
// One call for every volume on the host rather than one per volume: the daemon
// computes this by walking the filesystem, and asking it repeatedly for the same
// answer is how a panel that lists four volumes takes ten seconds to open.
//
// A driver that cannot measure a volume reports -1 for the size, and a daemon
// that cannot count references reports -1 for the count. Both are passed through
// as they are: "not available" is an answer, and turning it into 0 would read as
// an empty volume nothing uses, which is exactly the thing somebody would then
// delete.
func VolumeUsage(ctx context.Context, cli *client.Client) (map[string]VolumeSpace, error) {
	usage, err := cli.DiskUsage(ctx, types.DiskUsageOptions{
		Types: []types.DiskUsageObject{types.VolumeObject},
	})
	if err != nil {
		return nil, err
	}

	out := map[string]VolumeSpace{}
	for _, volume := range usage.Volumes {
		if volume == nil || volume.Name == "" {
			continue
		}

		space := VolumeSpace{Size: -1, Containers: -1}
		if volume.UsageData != nil {
			space.Size = volume.UsageData.Size
			space.Containers = volume.UsageData.RefCount
		}

		out[volume.Name] = space
	}

	return out, nil
}

// VolumeSpace is what the daemon can say about a volume's size and use.
type VolumeSpace struct {
	// Size in bytes, or -1 when the driver cannot measure it.
	Size int64
	// Containers referencing it, or -1 when the daemon cannot say.
	Containers int64
}
