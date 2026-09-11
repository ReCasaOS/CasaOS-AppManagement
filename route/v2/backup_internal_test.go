package v2

import (
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// Go's zero value for a bool is false, and false here means a copy taken from a
// running app -- so a request that simply forgot the field would quietly get the
// kind of backup that does not restore.
func TestAnOmittedHoldStillMeansStopTheApp(t *testing.T) {
	if !holdStillFrom(codegen.BackupRequest{Destination: "offsite"}) {
		t.Fatal("nil must mean the safe answer, not the zero one")
	}

	yes, no := true, false
	if !holdStillFrom(codegen.BackupRequest{HoldStill: &yes}) {
		t.Fatal("asked to stop")
	}
	if holdStillFrom(codegen.BackupRequest{HoldStill: &no}) {
		t.Fatal("asked not to: a hot copy is the caller's to choose")
	}
}

// A stamp names a run inside a destination, is read back by people in file
// browsers, and has to sort.
func TestTheStampSortsAndSurvivesAFilesystem(t *testing.T) {
	earlier := time.Date(2026, 9, 11, 2, 30, 0, 0, time.UTC).Format(stampLayout)
	later := time.Date(2026, 9, 11, 14, 5, 59, 0, time.UTC).Format(stampLayout)

	if earlier >= later {
		t.Fatalf("runs must sort by name: %q then %q", earlier, later)
	}

	// a colon is not a character every destination accepts in a path
	for _, bad := range []string{":", "/", "\\", " "} {
		if strings.Contains(later, bad) {
			t.Fatalf("%q contains %q", later, bad)
		}
	}
}
