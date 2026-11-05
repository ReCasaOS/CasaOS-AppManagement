package logstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type entry struct {
	at           time.Time
	stream, text string
}

func writeLines(t *testing.T, root, app, service, container string, lines []entry) {
	t.Helper()
	w := NewWriter(root, app, service, container, nil)
	for _, l := range lines {
		assert.NilError(t, w.Write(l.at, l.stream, l.text))
	}
	assert.NilError(t, w.Close())
}

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// walk reads every page, following Next until it is "".
func walk(t *testing.T, root string, q Query) []Line {
	t.Helper()
	var all []Line
	for range 1000 {
		p, err := Read(context.Background(), root, q)
		assert.NilError(t, err)
		all = append(all, p.Lines...)
		if p.Next == "" {
			return all
		}
		c, err := ParseCursor(p.Next)
		assert.NilError(t, err)
		if q.After != nil {
			q.After = &c
		} else {
			q.Before = &c
		}
	}
	t.Fatal("pages never ended")
	return nil
}

func TestPagesAreStableWithinOneSecond(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	for _, c := range []string{"a", "b"} {
		var lines []entry
		for i := range 5000 {
			lines = append(lines, entry{at, "o", fmt.Sprintf("%s-%04d", c, i)})
		}
		writeLines(t, root, "app", "web", strings.Repeat(c, 12), lines)
	}
	got := texts(walk(t, root, Query{Limit: 1000}))
	var want []string
	for _, c := range []string{"b", "a"} {
		for i := 4999; i >= 0; i-- {
			want = append(want, fmt.Sprintf("%s-%04d", c, i))
		}
	}
	assert.DeepEqual(t, got, want)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Read(ctx, root, Query{})
	assert.Assert(t, errors.Is(err, context.Canceled))
}

func TestForwardsFromACursorIsExclusive(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var lines []entry
	for i := range 10 {
		lines = append(lines, entry{base.Add(time.Duration(i) * time.Second), "o", fmt.Sprint(i)})
	}
	writeLines(t, root, "app", "web", "aaaaaaaaaaaa", lines)
	after := Cursor{At: base.Add(2 * time.Second), File: "app/web/2026-11-05.aaaaaaaaaaaa.log", Seq: 2}

	p, err := Read(context.Background(), root, Query{After: &after, Limit: 3})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(p.Lines), []string{"3", "4", "5"})
	assert.Assert(t, p.Next != "")
	next, err := ParseCursor(p.Next)
	assert.NilError(t, err)
	assert.Equal(t, next.Seq, 5)

	p, err = Read(context.Background(), root, Query{After: &next, Limit: 3})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(p.Lines), []string{"6", "7", "8"})
	next, err = ParseCursor(p.Next)
	assert.NilError(t, err)

	p, err = Read(context.Background(), root, Query{After: &next, Limit: 3})
	assert.NilError(t, err)
	assert.DeepEqual(t, texts(p.Lines), []string{"9"})
	assert.Equal(t, p.Next, "")
}

func TestSinceAndBeforeDoNotCombine(t *testing.T) {
	c := Cursor{At: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), File: "a/s/2026-11-05.aaaaaaaaaaaa.log"}
	_, err := Read(context.Background(), t.TempDir(), Query{After: &c, Before: &c})
	assert.Assert(t, errors.Is(err, ErrBothDirections))
}

func TestABadRegexIsABadFilter(t *testing.T) {
	_, err := Filter{Q: "(", Regex: true}.Compile()
	assert.Assert(t, errors.Is(err, ErrBadFilter))
}

func TestFiltersStreamLevelTextAndRegex(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	writeLines(t, root, "app", "web", "aaaaaaaaaaaa", []entry{
		{at, "o", "GET /api/12 200"},
		{at.Add(time.Second), "e", "something on stderr"},
		{at.Add(2 * time.Second), "o", "WARN disk almost full"},
		{at.Add(3 * time.Second), "o", "error: Disk failure"},
		{at.Add(4 * time.Second), "o", "hello GET /api/7"},
	})
	read := func(f Filter) []string {
		t.Helper()
		p, err := Read(context.Background(), root, Query{Filter: f})
		assert.NilError(t, err)
		return texts(p.Lines)
	}
	assert.DeepEqual(t, read(Filter{Stream: "e"}), []string{"something on stderr"})
	assert.DeepEqual(t, read(Filter{Level: "warn"}), []string{"error: Disk failure", "WARN disk almost full", "something on stderr"})
	assert.DeepEqual(t, read(Filter{Level: "error"}), []string{"error: Disk failure"})
	assert.DeepEqual(t, read(Filter{Q: "DISK"}), []string{"error: Disk failure", "WARN disk almost full"})
	assert.DeepEqual(t, read(Filter{Q: `^GET /api/\d+`, Regex: true}), []string{"GET /api/12 200"})
	assert.DeepEqual(t, read(Filter{Apps: []string{"other"}}), []string{})
	assert.Equal(t, len(read(Filter{Apps: []string{"app"}, Services: []string{"web"}})), 5)
}

func TestMidnightSplitsFilesNotTheTimeline(t *testing.T) {
	root := t.TempDir()
	late := time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC)
	early := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	writeLines(t, root, "app", "web", "aaaaaaaaaaaa", []entry{{late, "o", "late"}, {early, "o", "early"}})

	assert.DeepEqual(t, texts(walk(t, root, Query{Limit: 1})), []string{"early", "late"})
	start := Cursor{At: late.Add(-time.Hour), File: "app/web/2026-11-04.aaaaaaaaaaaa.log", Seq: -1}
	assert.DeepEqual(t, texts(walk(t, root, Query{After: &start, Limit: 1})), []string{"late", "early"})
}

func TestAPageNeverExceedsTwoMiB(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var lines []entry
	for i := range 300 {
		lines = append(lines, entry{at.Add(time.Duration(i) * time.Millisecond), "o", fmt.Sprintf("%03d ", i) + strings.Repeat("x", 15<<10)})
	}
	writeLines(t, root, "app", "web", "aaaaaaaaaaaa", lines)

	p, err := Read(context.Background(), root, Query{Limit: 500})
	assert.NilError(t, err)
	assert.Assert(t, len(p.Lines) < 300)
	assert.Assert(t, p.Next != "")
	b, err := json.Marshal(p)
	assert.NilError(t, err)
	assert.Assert(t, len(b) <= MaxPageBytes, len(b))

	all := walk(t, root, Query{Limit: 500})
	assert.Equal(t, len(all), 300)
	for i, l := range all {
		assert.Equal(t, l.Text[:3], fmt.Sprintf("%03d", 299-i))
	}
}

func TestListAppsReadsTheTree(t *testing.T) {
	root := t.TempDir()
	d4 := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	d5 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	writeLines(t, root, "media", "web", "aaaaaaaaaaaa", []entry{{d4, "o", "1"}, {d4.Add(time.Minute), "o", "2"}, {d5, "o", "3"}})
	writeLines(t, root, "media", "web", "bbbbbbbbbbbb", []entry{{d5.Add(time.Hour), "o", "4"}, {d5.Add(2 * time.Hour), "o", "5"}})
	writeLines(t, root, "media", "db", "cccccccccccc", []entry{{d5, "e", "6"}})
	writeLines(t, root, "notes", "app", "dddddddddddd", []entry{{d4, "o", "7"}})
	assert.NilError(t, AppendEvent(root, Event{At: d5, App: "media", Kind: "started", By: "docker"}))

	size := func(rels ...string) int64 {
		var n int64
		for _, r := range rels {
			st, err := os.Stat(filepath.Join(root, r))
			assert.NilError(t, err)
			n += st.Size()
		}
		return n
	}
	got, err := ListApps(root)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []AppSummary{
		{App: "media", Services: []ServiceSummary{
			{Service: "db", Container: "cccccccccccc", FirstAt: d5, LastAt: d5, SizeBytes: size("media/db/2026-11-05.cccccccccccc.log")},
			{Service: "web", Container: "bbbbbbbbbbbb", FirstAt: d4, LastAt: d5.Add(2 * time.Hour),
				SizeBytes: size("media/web/2026-11-04.aaaaaaaaaaaa.log", "media/web/2026-11-05.aaaaaaaaaaaa.log", "media/web/2026-11-05.bbbbbbbbbbbb.log")},
		}},
		{App: "notes", Services: []ServiceSummary{
			{Service: "app", Container: "dddddddddddd", FirstAt: d4, LastAt: d4, SizeBytes: size("notes/app/2026-11-04.dddddddddddd.log")},
		}},
	})
}

func TestCursorRoundTrips(t *testing.T) {
	c := Cursor{At: time.Date(2026, 11, 5, 10, 0, 0, 123456789, time.UTC), File: "app/web/2026-11-05.aaaaaaaaaaaa.log", Seq: 42}
	got, err := ParseCursor(c.String())
	assert.NilError(t, err)
	assert.DeepEqual(t, got, c)
	for _, bad := range []string{"", "x:1:a/b/c.log", "1:x:a/b/c.log", "1:-1:a/b/c.log", "1:2:", "1:2:../a/b.log", "1:2:/a/b.log", `1:2:a\b.log`} {
		_, err := ParseCursor(bad)
		assert.Assert(t, errors.Is(err, ErrBadFilter), bad)
	}
}
