package service

import (
	"context"
	"fmt"
	"sort"
)

// What a destination holds, for somebody restoring to a box that has nothing.
//
// The run log lists what THIS box did. A fresh install has an empty one, and
// the backups it needs are on the destination, filed the way RootFor files
// them: one folder per app, one folder per run inside it. Listing those two
// levels is the whole of the question; the manifests are not read here, since
// a destination holding a year of nightly runs would take a year of round
// trips to describe, and the restore reads the one manifest it needs.

// backupLister lists folders at a destination. Runs("", "") is the apps;
// Runs(app) is that app's runs.
type backupLister interface {
	Runs(destinationName, appPath string) ([]string, error)
}

// BackupHeldApp is one app a destination holds backups of.
type BackupHeldApp struct {
	App string `json:"app"`
	// Installed says whether this box runs the app now. A restore of one that is
	// not installs it first, from the backup's own compose file, which is worth
	// saying before the button is pressed.
	Installed bool `json:"installed"`
	// Stamps of its runs, newest first.
	Stamps []string `json:"stamps"`
}

// DestinationContents lists the apps a destination holds backups of, and their
// runs, newest first. installed is what this box runs now.
func DestinationContents(_ context.Context, lister backupLister, destination string, installed map[string]bool) ([]BackupHeldApp, error) {
	apps, err := lister.Runs(destination, "")
	if err != nil {
		return nil, fmt.Errorf("listing what `%s` holds: %w", destination, err)
	}
	sort.Strings(apps)

	held := make([]BackupHeldApp, 0, len(apps))
	for _, app := range apps {
		stamps, err := lister.Runs(destination, app)
		if err != nil {
			return nil, fmt.Errorf("listing the runs of `%s` at `%s`: %w", app, destination, err)
		}
		// A folder with no run inside it is not a backup of anything.
		if len(stamps) == 0 {
			continue
		}

		// Stamps are RFC 3339 with the colons swapped out, so the newest sorts last.
		sort.Sort(sort.Reverse(sort.StringSlice(stamps)))

		held = append(held, BackupHeldApp{App: app, Installed: installed[app], Stamps: stamps})
	}

	return held, nil
}
