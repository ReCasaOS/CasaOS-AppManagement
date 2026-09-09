package docker

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"gotest.tools/v3/assert"
)

// A recreated container must keep its anonymous volumes. They live only in
// containerInfo.Mounts, so the clone's HostConfig has to grow a mount for them
// or the daemon derives a fresh empty volume from the image VOLUME directive
// (`docker run -d --name pg postgres:16` -> empty /var/lib/postgresql/data).
func TestHostConfigCarriesAnonymousVolumes(t *testing.T) {
	namedMount := mount.Mount{Type: mount.TypeVolume, Source: "pgnamed", Target: "/named"}

	containerInfo := &types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				Binds:  []string{"/srv/conf:/etc/app:ro"},
				Mounts: []mount.Mount{namedMount},
			},
		},
		Mounts: []types.MountPoint{
			// anonymous volume from the image VOLUME directive - must be carried
			{
				Type:        mount.TypeVolume,
				Name:        "6f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8",
				Source:      "/var/lib/docker/volumes/6f1a.../_data",
				Destination: "/var/lib/postgresql/data",
				Driver:      "local",
				RW:          true,
			},
			// read-only anonymous volume - must be carried, read-only
			{
				Type:        mount.TypeVolume,
				Name:        "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900",
				Destination: "/cache",
				Driver:      "local",
			},
			// named volume already in HostConfig.Mounts - carrying it again conflicts
			{Type: mount.TypeVolume, Name: "pgnamed", Destination: "/named", RW: true},
			// bind mount already in HostConfig.Binds - carrying it again conflicts
			{Type: mount.TypeBind, Source: "/srv/conf", Destination: "/etc/app"},
			// tmpfs is not data
			{Type: mount.TypeTmpfs, Destination: "/tmp"},
		},
	}

	assert.DeepEqual(t, hostConfig(containerInfo).Mounts, []mount.Mount{
		namedMount,
		{
			Type:   mount.TypeVolume,
			Source: "6f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8",
			Target: "/var/lib/postgresql/data",
		},
		{
			Type:     mount.TypeVolume,
			Source:   "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900",
			Target:   "/cache",
			ReadOnly: true,
		},
	})
}

// Anything already in Binds must not come back as a second mount for the same
// destination -- and Binds is not only bind mounts: `-v pgdata:/data` puts a NAMED
// volume there, which arrives in containerInfo.Mounts as a TypeVolume with a name,
// exactly like the anonymous ones this carries. The destination is what tells them
// apart, and mounting one twice is a daemon-side conflict.
func TestHostConfigKeepsBindsUntouched(t *testing.T) {
	containerInfo := &types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			HostConfig: &container.HostConfig{Binds: []string{
				"/srv/data:/data",
				"pgdata:/var/lib/postgresql/data",
				"/srv/conf:/etc/app:ro",
			}},
		},
		Mounts: []types.MountPoint{
			{Type: mount.TypeBind, Source: "/srv/data", Destination: "/data", RW: true},
			{Type: mount.TypeVolume, Name: "pgdata", Destination: "/var/lib/postgresql/data", RW: true},
			{Type: mount.TypeBind, Source: "/srv/conf", Destination: "/etc/app"},
		},
	}

	assert.Equal(t, len(hostConfig(containerInfo).Mounts), 0)
}
