package service

import "testing"

// linuxserver.io tags every image `<upstream>-ls<build>`, and those builds cross 99
// into 100 constantly. SemVer compares alphanumeric prerelease identifiers letter by
// letter, so `ls99` sorts above `ls124` and the catalogue's older build reads as an
// upgrade -- a silent downgrade on the most common image family a home server runs.
func TestIsNewerTagOrdersBuildNumbersAsNumbers(t *testing.T) {
	for _, c := range []struct {
		candidate, current string
		want               bool
	}{
		{"1.23.1-ls124", "1.23.1-ls99", true},
		{"1.23.1-ls99", "1.23.1-ls124", false},
		{"1.23.1-ls10", "1.23.1-ls2", true},
		{"1.23.1-ls2", "1.23.1-ls10", false},
		{"1.23.1-ls124", "1.23.1-ls124", false},

		// a newer upstream wins whatever the build number does
		{"1.24.0-ls1", "1.23.1-ls999", true},
		{"1.23.1-ls999", "1.24.0-ls1", false},

		// plain versions are untouched
		{"1.23.2", "1.23.1", true},
		{"1.23.1", "1.23.2", false},
		{"2.0", "1.23.1", true},

		// nothing to order: the catalogue's word is all there is
		{"stable", "nightly", true},
		{"latest", "latest", false},
	} {
		if got := isNewerTag(c.candidate, c.current); got != c.want {
			t.Errorf("isNewerTag(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}
