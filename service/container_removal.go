package service

import (
	"fmt"
	"sort"
)

// Deciding what may be deleted along with a container.
//
// The only destructive path in this dashboard that reaches data, and the one
// place a mistake cannot be walked back: a container is made again from its
// image, a volume is not. So the decision is a pure function, apart from the
// daemon, and what it REFUSES is the part worth testing.

// ContainerVolume is one named volume a container mounts.
type ContainerVolume struct {
	// Name is the volume's name in Docker.
	Name string `json:"name"`
	// Target is where the container mounts it.
	Target string `json:"target"`
	// Size in bytes, or -1 when the driver cannot say. Shown so nobody deletes
	// 1.4 GB thinking it is a cache.
	Size int64 `json:"size"`
	// Containers is how many containers reference it, this one included, or -1
	// when the daemon cannot say.
	Containers int64 `json:"containers"`
	// Removable is false when this must not be offered for deletion at all.
	Removable bool `json:"removable"`
	// Reason says why, when it is not.
	Reason string `json:"reason,omitempty"`
}

// DescribeVolumesForRemoval marks which of a container's volumes may be offered.
//
// A volume another container still uses is never offered: deleting it takes data
// out from under something that is running, and the person clicking is looking at
// a card for a different container entirely.
//
// A daemon that cannot count references is treated as "cannot say", which is
// treated as "do not offer". Guessing wrong in that direction costs disk space;
// guessing wrong in the other costs somebody's database.
func DescribeVolumesForRemoval(volumes []ContainerVolume) []ContainerVolume {
	out := make([]ContainerVolume, 0, len(volumes))

	for _, volume := range volumes {
		switch {
		case volume.Name == "":
			volume.Removable = false
			volume.Reason = "an anonymous volume has no name to delete it by"

		case volume.Containers < 0:
			volume.Removable = false
			volume.Reason = "Docker could not say how many containers use this volume"

		case volume.Containers > 1:
			volume.Removable = false
			volume.Reason = fmt.Sprintf("%d other container(s) still use this volume", volume.Containers-1)

		default:
			volume.Removable = true
		}

		out = append(out, volume)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// VolumesToRemove filters what the caller asked to delete down to what may be.
//
// The caller's list is never trusted: a screen drawn a minute ago is a screen
// that has been overtaken, and by the time somebody presses the button another
// container may have started using one of these. Everything is re-decided here
// against what the daemon says now.
//
// A name the container does not mount at all is refused outright rather than
// passed through -- that is the shape of a request that would delete somebody
// else's volume through this container's door.
func VolumesToRemove(volumes []ContainerVolume, requested []string) (remove []string, refused map[string]string) {
	described := map[string]ContainerVolume{}
	for _, volume := range DescribeVolumesForRemoval(volumes) {
		described[volume.Name] = volume
	}

	refused = map[string]string{}
	seen := map[string]bool{}

	for _, name := range requested {
		if seen[name] {
			continue
		}
		seen[name] = true

		volume, mounted := described[name]
		if !mounted {
			refused[name] = "this container does not use that volume"
			continue
		}

		if !volume.Removable {
			refused[name] = volume.Reason
			continue
		}

		remove = append(remove, name)
	}

	sort.Strings(remove)

	return remove, refused
}
