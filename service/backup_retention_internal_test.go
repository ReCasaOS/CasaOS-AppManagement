package service

import (
	"strings"
	"testing"
)

// The only part of this feature that deletes anything, so what it would delete is
// a value that can be read before a single object is removed.
func TestWhatRetentionWouldDelete(t *testing.T) {
	// stamps sort, so the newest are simply the last
	runs := []string{
		"2026-09-08T03-00-00Z",
		"2026-09-09T03-00-00Z",
		"2026-09-10T03-00-00Z",
		"2026-09-11T03-00-00Z",
	}

	purge := RunsToPurge(runs, 2)
	if strings.Join(purge, ",") != "2026-09-08T03-00-00Z,2026-09-09T03-00-00Z" {
		t.Fatalf("keep the two newest: %v", purge)
	}
}

// A schedule saved without a retention means "no policy", not "keep none".
func TestNoRetentionDeletesNothing(t *testing.T) {
	runs := []string{"a", "b", "c"}

	for _, keep := range []int{0, -1, -100} {
		if purge := RunsToPurge(runs, keep); purge != nil {
			t.Fatalf("keep=%d deleted %v", keep, purge)
		}
	}
}

func TestFewerRunsThanTheLimitDeletesNothing(t *testing.T) {
	if purge := RunsToPurge([]string{"a", "b"}, 5); purge != nil {
		t.Fatalf("nothing to let go of yet: %v", purge)
	}
	if purge := RunsToPurge([]string{"a", "b"}, 2); purge != nil {
		t.Fatalf("exactly at the limit: %v", purge)
	}
	if purge := RunsToPurge(nil, 3); purge != nil {
		t.Fatalf("no runs at all: %v", purge)
	}
}

// A retention that empties a destination is not a retention.
func TestAtLeastOneRunAlwaysSurvives(t *testing.T) {
	runs := []string{"2026-09-08T03-00-00Z", "2026-09-09T03-00-00Z", "2026-09-10T03-00-00Z"}

	purge := RunsToPurge(runs, 1)
	if len(purge) != 2 {
		t.Fatalf("two go, one stays: %v", purge)
	}
	for _, gone := range purge {
		if gone == "2026-09-10T03-00-00Z" {
			t.Fatal("the newest is never the one deleted")
		}
	}
}

// The caller's slice is not the function's to rearrange.
func TestTheCallersListIsLeftAlone(t *testing.T) {
	runs := []string{"c", "a", "b"}
	RunsToPurge(runs, 1)

	if strings.Join(runs, ",") != "c,a,b" {
		t.Fatalf("the input was sorted underneath the caller: %v", runs)
	}
}
