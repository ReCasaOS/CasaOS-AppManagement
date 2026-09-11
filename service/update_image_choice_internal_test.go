package service

import "testing"

// The reported case: an app tracking a stream had its tag replaced by whatever
// fixed version the catalogue named. `develop` is not an omission somebody forgot
// to pin -- it is a choice to follow that stream, and an update pulls it rather
// than re-pinning it to something else.
func TestAnAppTrackingAStreamKeepsIt(t *testing.T) {
	for _, stream := range []string{
		"latest", "develop", "nightly", "edge", "main", "master",
		"stable", "beta", "alpha", "dev", "canary", "rolling",
		"unstable", "testing", "preview", "insiders",
	} {
		local := "linuxserver/qbittorrent:" + stream
		if updateWritesStoreImage(local, "linuxserver/qbittorrent:1.2.3") {
			t.Errorf("%s: an update must not replace a stream with a version", local)
		}
	}
}

func TestWhichImageAnUpdateWrites(t *testing.T) {
	cases := []struct {
		name         string
		local, store string
		write        bool
	}{
		{
			// what an update is for: both name a version, and the catalogue moved
			"version to newer version", "app:1.2.3", "app:1.2.4", true,
		},
		{
			// the reported bug, in one line
			"stream to version", "app:develop", "app:1.2.3", false,
		},
		{
			// a catalogue that floats says nothing about which version to run, so it
			// cannot un-pin somebody who pinned
			"version to stream", "app:1.2.3", "app:latest", false,
		},
		{
			"stream to stream", "app:latest", "app:latest", false,
		},
		{
			// a digest names one exact image; nothing about an update makes it another
			"digest stays", "app@sha256:abc123", "app:1.2.3", false,
		},
		{
			"nothing writes over a digest in the catalogue either", "app:1.2.3", "app@sha256:abc123", false,
		},
		{
			// no tag at all means latest, which is a stream
			"bare image is latest", "app", "app:1.2.3", false,
		},
		{
			// the colon here belongs to the port, not to a tag
			"a registry port is not a tag", "registry:5000/app", "registry:5000/app:1.2.3", false,
		},
		{
			"a registry port with real versions", "registry:5000/app:1.2.3", "registry:5000/app:1.2.4", true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := updateWritesStoreImage(c.local, c.store); got != c.write {
				t.Fatalf("local=%q store=%q: want write=%v, got %v", c.local, c.store, c.write, got)
			}
		})
	}
}

// The write and the decision to offer an update at all both read this. When they
// disagreed, the dashboard badged an app whose file the update then left exactly
// as it was, so the badge came back for ever -- which is why both call sites now
// pass both references.
//
// The rule is "both are fixed", so it reads the same whichever way round a caller
// passes them. That is worth pinning: it is what makes a call site that swaps its
// arguments harmless rather than silently inverting the answer for exactly the
// case this fixes.
func TestTheRuleReadsTheSameWhicheverWayRound(t *testing.T) {
	pairs := [][2]string{
		{"app:develop", "app:1.2.3"},
		{"app:1.2.3", "app:1.2.4"},
		{"app:latest", "app:1.2.3"},
		{"app@sha256:abc123", "app:1.2.3"},
	}

	for _, p := range pairs {
		if updateWritesStoreImage(p[0], p[1]) != updateWritesStoreImage(p[1], p[0]) {
			t.Errorf("%q and %q answer differently depending on the order", p[0], p[1])
		}
	}
}
