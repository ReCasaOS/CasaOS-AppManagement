package logstore

import (
	"context"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := Open(ctx, t.TempDir())
	assert.NilError(t, err)
	t.Cleanup(func() {
		assert.NilError(t, s.Close())
		cancel()
	})
	return s
}

func TestWhatIsWrittenIsReadAndStreamed(t *testing.T) {
	s := openTest(t)
	ch, cancel := s.Subscribe(nil)
	defer cancel()

	at := time.Now().UTC().Truncate(time.Second)
	w := s.Writer("plex", "plex", "0123456789abcdef")
	assert.NilError(t, w.Write(at, "o", "hello"))
	assert.NilError(t, s.Record(Event{At: at, App: "plex", Service: "plex", Container: "0123456789ab", Kind: "started", By: "docker"}))
	assert.NilError(t, s.CloseWriter("0123456789ab"))

	for _, want := range []string{"line", "event"} {
		select {
		case m := <-ch:
			assert.Equal(t, m.T, want)
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s message", want)
		}
	}

	page, err := s.Read(context.Background(), Query{})
	assert.NilError(t, err)
	assert.Equal(t, len(page.Lines), 1)
	assert.Equal(t, page.Lines[0].Text, "hello")

	evs, err := s.Events(EventQuery{})
	assert.NilError(t, err)
	assert.Equal(t, len(evs), 1)
	assert.Equal(t, evs[0].Kind, "started")
}

func TestTheSameContainerGetsTheSameWriter(t *testing.T) {
	s := openTest(t)
	a := s.Writer("a", "s", "0123456789abcdef")
	assert.Assert(t, a == s.Writer("a", "s", "0123456789abXXXX"))
	assert.Assert(t, a != s.Writer("a", "s", "ffffffffffff"))
}

func TestCloseWriterFlushesAndForgets(t *testing.T) {
	s := openTest(t)
	at := time.Now().UTC().Truncate(time.Second)
	w := s.Writer("a", "s", "0123456789ab")
	assert.NilError(t, w.Write(at, "o", "first"))
	assert.NilError(t, s.CloseWriter("0123456789ab"))
	assert.NilError(t, s.CloseWriter("unknown"))

	w2 := s.Writer("a", "s", "0123456789ab")
	assert.Assert(t, w != w2)
	assert.NilError(t, w2.Write(at.Add(time.Second), "o", "second"))
	assert.NilError(t, s.CloseWriter("0123456789ab"))

	page, err := s.Read(context.Background(), Query{})
	assert.NilError(t, err)
	assert.Equal(t, len(page.Lines), 2)
}
