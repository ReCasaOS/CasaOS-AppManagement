package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A destination as the listing sees it: folders under folders.
type fakeLister struct {
	folders map[string][]string // path -> the folders inside it
	fail    map[string]bool     // paths that cannot be listed; the root is ""
}

func (f *fakeLister) Runs(_ string, appPath string) ([]string, error) {
	if f.fail[appPath] {
		return nil, errors.New("rclone: directory not found")
	}

	return f.folders[appPath], nil
}

func TestADestinationListsItsAppsAndTheirRunsNewestFirst(t *testing.T) {
	lister := &fakeLister{folders: map[string][]string{
		"":          {"nextcloud", "jellyfin"},
		"jellyfin":  {"2026-09-01T02-00-00Z", "2026-09-13T02-00-00Z", "2026-09-07T02-00-00Z"},
		"nextcloud": {"2026-09-12T02-00-00Z"},
	}}

	held, err := DestinationContents(context.Background(), lister, "offsite", map[string]bool{"jellyfin": true})
	if err != nil {
		t.Fatal(err)
	}

	if len(held) != 2 || held[0].App != "jellyfin" || held[1].App != "nextcloud" {
		t.Fatalf("apps, in order: %+v", held)
	}
	if strings.Join(held[0].Stamps, ",") != "2026-09-13T02-00-00Z,2026-09-07T02-00-00Z,2026-09-01T02-00-00Z" {
		t.Fatalf("newest first: %v", held[0].Stamps)
	}
	if !held[0].Installed || held[1].Installed {
		t.Fatalf("jellyfin runs here, nextcloud does not: %+v", held)
	}
}

// A folder with no run inside it is not a backup of anything, and is not offered.
func TestAnAppFolderWithNoRunIsNotOffered(t *testing.T) {
	lister := &fakeLister{folders: map[string][]string{
		"":      {"empty", "real"},
		"real":  {"2026-09-13T02-00-00Z"},
		"empty": {},
	}}

	held, err := DestinationContents(context.Background(), lister, "offsite", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(held) != 1 || held[0].App != "real" {
		t.Fatalf("%+v", held)
	}
}

// A destination that cannot be listed is an error, not an empty list -- an empty
// list reads as "nothing to restore", which is the wrong thing to tell somebody
// on a fresh box.
func TestADestinationThatCannotBeListedIsAnError(t *testing.T) {
	lister := &fakeLister{fail: map[string]bool{"": true}}

	if _, err := DestinationContents(context.Background(), lister, "offsite", nil); err == nil {
		t.Fatal("want the error")
	}
}
