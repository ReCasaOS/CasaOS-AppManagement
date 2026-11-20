package service

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

func TestNewerTagsAreTheHighestOfEachLevelOfTheSameFlavour(t *testing.T) {
	tags := []string{
		"1.4.1", "1.4.2", "1.4.3", "1.4.10", "1.5.0", "1.6.2", "2.0.0", "2.1.0",
		"1.4.4-alpine", "1.7.0-alpine", // another flavour
		"1.4.5-rc1", "3.0.0-beta.2", // prereleases
		"1.4", "latest", "main", "v1.9.0", // another shape, no version, another prefix
	}

	patch, minor, major := newerTagsOf("1.4.2", tags)
	assert.Equal(t, patch, "1.4.10")
	assert.Equal(t, minor, "1.6.2")
	assert.Equal(t, major, "2.1.0")

	patch, minor, major = newerTagsOf("1.4.2-alpine", tags)
	assert.Equal(t, patch, "1.4.4-alpine")
	assert.Equal(t, minor, "1.7.0-alpine")
	assert.Equal(t, major, "")
}

func TestNothingNewerIsNothing(t *testing.T) {
	for name, current := range map[string]string{
		"already the highest": "2.1.0",
		"not a version":       "latest",
		"a word after numbers": "1.4.2x",
	} {
		t.Run(name, func(t *testing.T) {
			patch, minor, major := newerTagsOf(current, []string{"1.4.2", "2.1.0", "latest"})
			assert.Equal(t, patch+minor+major, "")
		})
	}
}

// linuxserver tags every build: `-ls245` is a build number, not a flavour.
func TestABuildNumberInTheSuffixIsOneFlavourAndOrdered(t *testing.T) {
	tags := []string{"10.9.11-ls244", "10.9.11-ls246", "10.9.12-ls247", "10.10.0-ls250", "10.9.11-alpine"}

	patch, minor, major := newerTagsOf("10.9.11-ls245", tags)
	assert.Equal(t, patch, "10.9.12-ls247")
	assert.Equal(t, minor, "10.10.0-ls250")
	assert.Equal(t, major, "")
}

func TestAPrereleaseIsOfferedOnlyToAPrerelease(t *testing.T) {
	tags := []string{"2.0.0-rc1", "2.0.0-rc2", "2.0.0"}

	_, _, major := newerTagsOf("1.9.0", tags)
	assert.Equal(t, major, "2.0.0")

	patch, _, _ := newerTagsOf("2.0.0-rc1", tags)
	assert.Equal(t, patch, "2.0.0-rc2")
}

func TestThePrefixAndTheNumberOfPartsAreKept(t *testing.T) {
	patch, minor, major := newerTagsOf("v1.4", []string{"v1.5", "v2.0", "1.6", "v1.4.1"})
	assert.Equal(t, patch, "")
	assert.Equal(t, minor, "v1.5")
	assert.Equal(t, major, "v2.0")
}

// One request per repository, whatever number of apps run it, and a registry that does
// not answer leaves its images out without failing the others.
func TestAPassAsksEachRepositoryOnceAndKeepsWhatItFound(t *testing.T) {
	wasList, wasPinned := listTags, pinnedImages
	t.Cleanup(func() { listTags, pinnedImages = wasList, wasPinned })

	asked := map[string]int{}
	listTags = func(_ context.Context, image string) ([]string, error) {
		asked[image]++
		if image == "ghcr.io/down/app:1.0.0" {
			return nil, errors.New("registry down")
		}
		return []string{"16.2", "16.4", "17.0", "1.2.3", "1.3.0"}, nil
	}
	pinnedImages = func(context.Context) (map[string]map[string]string, error) {
		return map[string]map[string]string{
			"immich":   {"db": "postgres:16.2"},
			"nextcloud": {"db": "postgres:16.2", "app": "nextcloud:1.2.3"},
			"down":     {"web": "ghcr.io/down/app:1.0.0"},
		}, nil
	}

	CheckNewerTags(context.Background())

	assert.Equal(t, asked["postgres:16.2"], 1)
	immich := NewerTagsOf("immich")
	assert.Equal(t, len(immich), 1)
	assert.Equal(t, *immich[0].Minor, "16.4")
	assert.Equal(t, *immich[0].Major, "17.0")
	assert.Assert(t, immich[0].Patch == nil)
	assert.Equal(t, len(NewerTagsOf("nextcloud")), 2)
	assert.Assert(t, NewerTagsOf("down") == nil)
}

// linuxserver/jellyfin holds tags like `2021.12.16` from when it tagged by date: read
// from a real registry, they came out as a major version above 10.9.11.
func TestTagsByDateAreAnotherLine(t *testing.T) {
	tags := []string{"10.10.7", "10.11.11", "2021.12.16", "2024.1.2"}

	_, minor, major := newerTagsOf("10.9.11", tags)
	assert.Equal(t, minor, "10.11.11")
	assert.Equal(t, major, "")

	// an app that runs a dated tag is offered the dated ones
	_, _, major = newerTagsOf("2021.12.16", tags)
	assert.Equal(t, major, "2024.1.2")
}
