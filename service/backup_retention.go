package service

import "sort"

// Keeping a few backups and letting go of the rest.
//
// This is the only part of the feature that deletes anything, which is reason
// enough for it to be a pure function with the decision separated from the doing:
// what gets purged can be read, tested and shown to somebody before a single
// object is removed.

// RunsToPurge names the runs that fall outside a retention of `keep`.
//
// Runs are named by their stamp, which sorts, so the newest are simply the last.
// `keep` of zero or less keeps everything -- deleting backups is not something to
// do because a field defaulted, and a schedule saved without a retention means
// "no policy", not "keep none".
//
// One run is always kept whatever the arithmetic says. A retention that empties a
// destination is not a retention.
func RunsToPurge(stamps []string, keep int) []string {
	if keep <= 0 || len(stamps) <= keep {
		return nil
	}

	ordered := make([]string, len(stamps))
	copy(ordered, stamps)
	sort.Strings(ordered)

	// everything but the last `keep`
	return ordered[:len(ordered)-keep]
}
