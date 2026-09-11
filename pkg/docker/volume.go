package docker

import (
	"context"

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
