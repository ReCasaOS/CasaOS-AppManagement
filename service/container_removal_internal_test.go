package service

import (
	"strings"
	"testing"
)

func vol(name string, size, containers int64) ContainerVolume {
	return ContainerVolume{Name: name, Target: "/data", Size: size, Containers: containers}
}

// Deleting a volume another container still uses takes data out from under
// something that is running, and the person clicking is looking at a card for a
// different container entirely.
func TestAVolumeSomethingElseUsesIsNeverOffered(t *testing.T) {
	described := DescribeVolumesForRemoval([]ContainerVolume{
		vol("pgdata", 1_400_000_000, 1),
		vol("shared", 12_000_000, 3),
	})

	if !described[0].Removable {
		t.Fatalf("only this container uses pgdata: %+v", described[0])
	}
	if described[1].Removable {
		t.Fatal("three containers use shared")
	}
	if !strings.Contains(described[1].Reason, "2 other") {
		t.Fatalf("say how many: %q", described[1].Reason)
	}
}

// Guessing wrong in this direction costs disk space; guessing wrong in the other
// costs somebody's database.
func TestADaemonThatCannotCountIsTreatedAsCannotSay(t *testing.T) {
	described := DescribeVolumesForRemoval([]ContainerVolume{vol("mystery", -1, -1)})

	if described[0].Removable {
		t.Fatal("an unknown reference count is not permission")
	}
	if !strings.Contains(described[0].Reason, "could not say") {
		t.Fatalf("%q", described[0].Reason)
	}
}

func TestAnAnonymousVolumeHasNoNameToDeleteItBy(t *testing.T) {
	described := DescribeVolumesForRemoval([]ContainerVolume{vol("", 500, 1)})

	if described[0].Removable {
		t.Fatal("nothing names it")
	}
}

// A screen drawn a minute ago is a screen that has been overtaken.
func TestTheCallersListIsNeverTrusted(t *testing.T) {
	volumes := []ContainerVolume{
		vol("pgdata", 1_400_000_000, 1),
		vol("shared", 12_000_000, 4),
	}

	remove, refused := VolumesToRemove(volumes, []string{"pgdata", "shared"})

	if strings.Join(remove, ",") != "pgdata" {
		t.Fatalf("only the one that is free: %v", remove)
	}
	if refused["shared"] == "" {
		t.Fatal("and the other is refused with a reason")
	}
}

// The shape of a request that would delete somebody else's volume through this
// container's door.
func TestAVolumeThisContainerDoesNotUseIsRefusedOutright(t *testing.T) {
	remove, refused := VolumesToRemove([]ContainerVolume{vol("pgdata", 100, 1)},
		[]string{"pgdata", "someone-elses-database"})

	if strings.Join(remove, ",") != "pgdata" {
		t.Fatalf("%v", remove)
	}
	if !strings.Contains(refused["someone-elses-database"], "does not use") {
		t.Fatalf("%q", refused["someone-elses-database"])
	}
}

func TestAskingForTheSameVolumeTwiceRemovesItOnce(t *testing.T) {
	remove, _ := VolumesToRemove([]ContainerVolume{vol("pgdata", 100, 1)},
		[]string{"pgdata", "pgdata", "pgdata"})

	if len(remove) != 1 {
		t.Fatalf("%v", remove)
	}
}

func TestAskingForNothingRemovesNothing(t *testing.T) {
	remove, refused := VolumesToRemove([]ContainerVolume{vol("pgdata", 100, 1)}, nil)

	if len(remove) != 0 || len(refused) != 0 {
		t.Fatalf("remove=%v refused=%v", remove, refused)
	}
}
