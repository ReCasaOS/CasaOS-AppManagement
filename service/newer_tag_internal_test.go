package service

import (
	"strings"
	"testing"
)

// linuxserver.io tags every image `<upstream>-ls<build>`, and those builds cross 99
// into 100 constantly. SemVer compares alphanumeric prerelease identifiers letter by
// letter, so `ls99` sorts above `ls124` and the catalogue's older build reads as an
// upgrade -- a silent downgrade on the most common image family a home server runs.
//
// The four-component form is the same vendor and worse: SemVer cannot parse it at
// all, so a comparison that gave up there ordered nothing, and every one of those
// tags read as an upgrade in both directions.
func TestCompareTagsOrdersBuildNumbersAsNumbers(t *testing.T) {
	for _, c := range []struct {
		a, b    string
		want    int
		ordered bool
	}{
		{"1.23.1-ls124", "1.23.1-ls99", 1, true},
		{"1.23.1-ls99", "1.23.1-ls124", -1, true},
		{"1.23.1-ls10", "1.23.1-ls2", 1, true},
		{"1.23.1-ls2", "1.23.1-ls10", -1, true},
		{"1.23.1-ls124", "1.23.1-ls124", 0, true},

		// a newer upstream wins whatever the build number does
		{"1.24.0-ls1", "1.23.1-ls999", 1, true},
		{"1.23.1-ls999", "1.24.0-ls1", -1, true},

		// four components: no SemVer at all, and the exact tags linuxserver.io ships
		{"1.40.2.8395-c67dce28e-ls123", "1.40.2.8395-c67dce28e-ls100", 1, true},
		{"1.40.2.8395-c67dce28e-ls100", "1.40.2.8395-c67dce28e-ls123", -1, true},
		{"1.40.3.8555-a1b2c3d4e-ls1", "1.40.2.8395-c67dce28e-ls123", 1, true},
		{"v1.40.2.8395-c67dce28e-ls123", "1.40.2.8395-c67dce28e-ls100", 1, true},

		// plain versions are untouched
		{"1.23.2", "1.23.1", 1, true},
		{"1.23.1", "1.23.2", -1, true},
		{"2.0", "1.23.1", 1, true},

		// SemVer still orders prerelease words, where digit runs say nothing
		{"1.2.3-beta", "1.2.3-alpha", 1, true},

		// nothing orders these, and saying so is the point: a guess here is what
		// writes an older image over a working app
		{"stable", "nightly", 0, false},
		{"latest", "1.2.3", 0, false},
		{"1.40.2.8395-c67dce28e-ls1", "1.40.2.8395-ffffffff-ls1", 0, false},
		{"latest", "latest", 0, true},
	} {
		got, ordered := compareTags(c.a, c.b)
		if got != c.want || ordered != c.ordered {
			t.Errorf("compareTags(%q, %q) = %d, %v, want %d, %v", c.a, c.b, got, ordered, c.want, c.ordered)
		}
	}
}

// What the ordering is for: an update writes the catalogue's image over the running
// one, so a catalogue that has fallen behind -- or that names a tag nothing can order
// against the running one -- must be refused, and must say which images it is about.
func TestBackwardsReasonRefusesWhatItCannotShowIsForward(t *testing.T) {
	for _, c := range []struct {
		local, store string
		refused      bool
	}{
		// the measured regression: a catalogue on ls100 against a running ls123
		{"lscr.io/linuxserver/plex:1.40.2.8395-c67dce28e-ls123", "lscr.io/linuxserver/plex:1.40.2.8395-c67dce28e-ls100", true},
		{"lscr.io/linuxserver/plex:1.40.2.8395-c67dce28e-ls100", "lscr.io/linuxserver/plex:1.40.2.8395-c67dce28e-ls123", false},

		{"acme/app:2.0", "acme/app:1.8", true},
		{"acme/app:1.8", "acme/app:2.0", false},
		{"acme/app:2.0", "acme/app:2.0", false},

		// a channel is not a version: neither direction can be shown to be forward
		{"acme/app:stable", "acme/app:nightly", true},
		{"acme/app:nightly", "acme/app:stable", true},

		// a digest pin has no tag to order; the catalogue's reference is the statement
		{"acme/app@sha256:aaaa", "acme/app:2.0", false},
		{"acme/app:2.0", "acme/app@sha256:aaaa", false},

		// the same tag on another repository is a move, not a rollback
		{"acme/app:2.0", "ghcr.io/acme/app:2.0", false},
	} {
		why := backwardsReason(c.local, c.store)
		if (why != "") != c.refused {
			t.Errorf("backwardsReason(%q, %q) = %q, refused = %v, want refused = %v", c.local, c.store, why, why != "", c.refused)
		}
		if why != "" && (!strings.Contains(why, c.local) || !strings.Contains(why, c.store)) {
			t.Errorf("backwardsReason(%q, %q) = %q, which does not name both images", c.local, c.store, why)
		}
	}
}
