package logstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

func TestAPurgedFileIsCreatedAgainByItsWriter(t *testing.T) {
	s := openTest(t)
	at := time.Now().UTC()
	w := s.Writer("a", "s", "0123456789ab")
	assert.NilError(t, w.Write(at, "o", "before"))
	assert.NilError(t, w.Flush())

	_, err := s.Purge(at, 0, 1) // a 1-byte cap: today's file goes
	assert.NilError(t, err)
	used, _, err := s.Usage()
	assert.NilError(t, err)
	assert.Equal(t, used, int64(0))

	assert.NilError(t, w.Write(at.Add(time.Millisecond), "o", "after"))
	assert.NilError(t, w.Flush())
	page, err := s.Read(context.Background(), Query{})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(page.Lines), []string{"after"})
	used, _, err = s.Usage()
	assert.NilError(t, err)
	assert.Assert(t, used > 0)
}

func TestAForwardsReadFlushesEveryWriterAndHoldsTheLastSecond(t *testing.T) {
	s := openTest(t)
	at := time.Now().UTC()
	// B has no flush loop: its older line stays in its buffer until the read flushes it
	b := NewWriter(s.Root, "app", "b", "bbbbbbbbbbbb", nil)
	s.mu.Lock()
	s.writers["bbbbbbbbbbbb"] = b
	s.mu.Unlock()
	assert.NilError(t, b.Write(at.Add(-500*time.Millisecond), "o", "b"))
	a := s.Writer("app", "a", "aaaaaaaaaaaa")
	assert.NilError(t, a.Write(at.Add(-100*time.Millisecond), "o", "a"))
	assert.NilError(t, a.Flush())
	start := Cursor{At: at.Add(-time.Hour)}

	s.now = func() time.Time { return at }
	p, err := s.Read(context.Background(), Query{After: &start})
	assert.NilError(t, err)
	assert.Equal(t, len(p.Lines), 0) // both are within the last second
	assert.Equal(t, p.Next, "")

	s.now = func() time.Time { return at.Add(2 * time.Second) }
	p, err = s.Read(context.Background(), Query{After: &start})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(p.Lines), []string{"b", "a"})
}

func TestOpenNeverCreatesTheRootsGrandparent(t *testing.T) {
	tmp := t.TempDir()
	_, err := Open(context.Background(), filepath.Join(tmp, "missing", "sub", "logs"))
	assert.Assert(t, err != nil)
	_, err = os.Stat(filepath.Join(tmp, "missing"))
	assert.Assert(t, errors.Is(err, fs.ErrNotExist), err)

	assert.NilError(t, os.Mkdir(filepath.Join(tmp, "present"), 0o755))
	_, err = Open(context.Background(), filepath.Join(tmp, "present", "sub", "logs"))
	assert.NilError(t, err)
	st, err := os.Stat(filepath.Join(tmp, "present", "sub", "logs"))
	assert.NilError(t, err)
	assert.Assert(t, st.IsDir())
}

func TestASymlinkedRootIsPurged(t *testing.T) {
	tmp := t.TempDir()
	target, link := filepath.Join(tmp, "target"), filepath.Join(tmp, "link")
	assert.NilError(t, os.Mkdir(target, 0o755))
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Open(ctx, link)
	assert.NilError(t, err)
	defer s.Close()
	old := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	writeLines(t, s.Root, "a", "s", "aaaaaaaaaaaa", []entry{{old, "o", "old"}})

	_, oldest, err := s.Usage()
	assert.NilError(t, err)
	assert.Assert(t, oldest.Equal(d("2026-01-01")), oldest)
	removed, err := s.Purge(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), 7, 0)
	assert.NilError(t, err)
	assert.Assert(t, removed > 0)
	_, err = os.Stat(DayFile(target, "a", "s", "aaaaaaaaaaaa", old))
	assert.Assert(t, errors.Is(err, fs.ErrNotExist), err)
}
