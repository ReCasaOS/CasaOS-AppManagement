package logstore

import (
	"context"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestNotAppsLeavesOutLinesAndEvents(t *testing.T) {
	root, _ := seedEvents(t)
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	writeLines(t, root, "plex", "plex", "aaaaaaaaaaaa", []entry{{at, "o", "from plex"}})
	writeLines(t, root, "nextcloud", "app", "bbbbbbbbbbbb", []entry{{at, "o", "from nextcloud"}})

	page, err := Read(context.Background(), root, Query{Filter: Filter{NotApps: []string{"nextcloud"}}, Since: at.Add(-24 * time.Hour), Until: at.Add(24 * time.Hour)})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(page.Lines), []string{"from plex"})
	for _, e := range page.Events {
		assert.Assert(t, e.App != "nextcloud")
	}

	events, err := ReadEvents(root, EventQuery{NotApps: []string{"nextcloud"}})
	assert.NilError(t, err)
	assert.Equal(t, len(events), 2)
	for _, e := range events {
		assert.Equal(t, e.App, "plex")
	}
}

func TestNotAppsKeepsASubscriberFromHiddenApps(t *testing.T) {
	h := NewHub()
	m, err := Filter{NotApps: []string{"immich"}}.Compile()
	assert.NilError(t, err)
	ch, cancel := h.Subscribe(m)
	defer cancel()

	h.Publish(lineMsg("immich", "no"))
	h.Publish(Message{T: "event", Event: &Event{App: "immich", Kind: "died"}})
	h.Publish(lineMsg("_other", "yes"))

	assert.Equal(t, recv(t, ch).Line.Text, "yes")
}
