package logstore

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Filter struct {
	Apps, Services []string
	Stream         string // "", "o", "e"
	Level          string // "", "info", "warn", "error" (minimum level)
	Q              string
	Regex          bool
}

var (
	ErrBadFilter      = errors.New("bad filter")
	ErrBothDirections = errors.New("since and before do not combine")
)

var levelRank = map[string]int{"": 0, "info": 0, "warn": 1, "error": 2}

type Matcher struct {
	apps, services map[string]bool // SafeName'd, nil = any
	stream         string
	minLevel       int
	re             *regexp.Regexp
}

func (f Filter) Compile() (*Matcher, error) {
	rank, ok := levelRank[f.Level]
	if !ok {
		return nil, fmt.Errorf("%w: level %q", ErrBadFilter, f.Level)
	}
	if f.Stream != "" && f.Stream != "o" && f.Stream != "e" {
		return nil, fmt.Errorf("%w: stream %q", ErrBadFilter, f.Stream)
	}
	m := &Matcher{apps: nameSet(f.Apps), services: nameSet(f.Services), stream: f.Stream, minLevel: rank}
	if f.Q != "" {
		expr := "(?i)" + regexp.QuoteMeta(f.Q)
		if f.Regex {
			expr = f.Q
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadFilter, err)
		}
		m.re = re
	}
	return m, nil
}

func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[SafeName(n)] = true
	}
	return set
}

func inSet(set map[string]bool, name string) bool {
	return set == nil || set[name] || set[SafeName(name)]
}

func (m *Matcher) Match(l Line) bool {
	return inSet(m.apps, l.App) && inSet(m.services, l.Service) &&
		(m.stream == "" || l.Stream == m.stream) &&
		(m.minLevel == 0 || levelRank[GuessLevel(l.Stream, l.Text)] >= m.minLevel) &&
		(m.re == nil || m.re.MatchString(l.Text))
}

func (m *Matcher) MatchEvent(e Event) bool { return inSet(m.apps, e.App) }

type Cursor struct {
	At   time.Time
	File string // path relative to root
	Seq  int    // 0-based line index in File
}

func (c Cursor) String() string {
	return strconv.FormatInt(c.At.UnixNano(), 10) + ":" + strconv.Itoa(c.Seq) + ":" + c.File
}

// ParseCursor wraps its error in ErrBadFilter: a cursor comes from the client like the filter does.
func ParseCursor(s string) (Cursor, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) == 3 {
		ns, err1 := strconv.ParseInt(parts[0], 10, 64)
		seq, err2 := strconv.Atoi(parts[1])
		file := parts[2]
		if err1 == nil && err2 == nil && seq >= 0 && file != "" && !strings.HasPrefix(file, "/") &&
			!strings.Contains(file, `\`) && !slices.Contains(strings.Split(file, "/"), "..") {
			return Cursor{At: time.Unix(0, ns).UTC(), File: file, Seq: seq}, nil
		}
	}
	return Cursor{}, fmt.Errorf("%w: cursor %q", ErrBadFilter, s)
}

type Query struct {
	Filter
	Since  time.Time // with After: lower bound of the window; otherwise zero
	Until  time.Time
	Before *Cursor // backwards (newest→oldest) strictly before; nil = from the end
	After  *Cursor // forwards (oldest→newest) strictly after; nil = backwards mode
	Limit  int     // clamped to [1,1000], default 500
}

const MaxPageBytes = 2 << 20

type Page struct {
	Lines  []Line  `json:"lines"`
	Events []Event `json:"events"`
	Next   string  `json:"next,omitempty"` // "" when exhausted
}

const (
	readChunk   = 64 << 10
	maxReadLine = 1 << 20 // a longer line is cut, then Truncate cuts it to MaxLineBytes
	// The page cap counts len(Text)+len(App)+len(Service)+lineJSONOverhead per line.
	// shortcut: JSON escapes (control chars, <>&) and the events are not counted; measure the encoding if pages near the cap get rejected.
	lineJSONOverhead = 128
)

// Read returns one page of history. Lines are ordered on (At, File, Seq), descending without After,
// ascending with it; a day file's lines are assumed to be in time order, as the writer appends them.
func Read(ctx context.Context, root string, q Query) (Page, error) {
	if q.After != nil && q.Before != nil {
		return Page{}, ErrBothDirections
	}
	m, err := q.Compile()
	if err != nil {
		return Page{}, err
	}
	p := pager{q: q, m: m, limit: q.Limit, lines: []Line{}}
	if p.limit <= 0 {
		p.limit = 500
	}
	p.limit = min(p.limit, 1000)

	files, err := listLogs(root, m.apps, m.services)
	if err != nil {
		return Page{}, err
	}
	lo, hi := dayOfTime(q.Since), dayOfTime(q.Until) // "" = unbounded
	if q.After != nil {
		lo = max(lo, dayOfTime(q.After.At))
	} else if q.Before != nil {
		if d := dayOfTime(q.Before.At); hi == "" || d < hi {
			hi = d
		}
	}
	byDay := map[string][]logFile{}
	var days []string
	for _, f := range files {
		if (lo != "" && f.day < lo) || (hi != "" && f.day > hi) {
			continue
		}
		if byDay[f.day] == nil {
			days = append(days, f.day)
		}
		byDay[f.day] = append(byDay[f.day], f)
	}
	sort.Strings(days)
	if q.After == nil {
		slices.Reverse(days)
	}
	for _, d := range days {
		if err := p.readDay(ctx, root, byDay[d]); err != nil {
			return Page{}, err
		}
		if p.done {
			break
		}
	}

	page := Page{Lines: p.lines, Events: []Event{}}
	if p.full {
		page.Next = p.last.String()
	}
	if n := len(p.lines); n > 0 {
		oldest, newest := p.lines[0].At, p.lines[n-1].At
		if q.After == nil {
			oldest, newest = newest, oldest
		}
		page.Events, err = ReadEvents(root, EventQuery{Apps: q.Apps, Since: oldest, Until: newest, Limit: 1000})
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func dayOfTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

type pager struct {
	q     Query
	m     *Matcher
	limit int
	lines []Line
	bytes int
	last  Cursor
	full  bool // stopped on Limit or MaxPageBytes: Next is set
	done  bool // full, or past the Since/Until bound
}

// readDay merges the day's files on (At, File, Seq), in the direction of the query.
func (p *pager) readDay(ctx context.Context, root string, files []logFile) error {
	var srcs []*source
	defer func() {
		for _, s := range srcs {
			s.f.Close()
		}
	}()
	for _, lf := range files {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(lf.rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // purged since the listing
		}
		if err != nil {
			return err
		}
		s := &source{f: f, file: lf}
		srcs = append(srcs, s)
		if p.q.After != nil {
			s.lineReader = &fwdReader{r: bufio.NewReaderSize(ctxReader{ctx, f}, readChunk)}
		} else if s.lineReader, err = newRevReader(ctx, f); err != nil {
			return err
		}
		if err := s.advance(); err != nil {
			return err
		}
	}
	for !p.done {
		var best *source
		for _, s := range srcs {
			if s.ok && (best == nil || p.first(s, best)) {
				best = s
			}
		}
		if best == nil {
			return nil
		}
		p.take(best)
		if err := best.advance(); err != nil {
			return err
		}
	}
	return nil
}

func compareTo(at time.Time, file string, seq int, c Cursor) int {
	return cmp.Or(at.Compare(c.At), strings.Compare(file, c.File), cmp.Compare(seq, c.Seq))
}

// first reports whether a's head comes before b's in the walk order.
func (p *pager) first(a, b *source) bool {
	c := compareTo(a.head.at, a.file.rel, a.head.seq, Cursor{At: b.head.at, File: b.file.rel, Seq: b.head.seq})
	return (c < 0) == (p.q.After != nil)
}

func (p *pager) take(s *source) {
	r, q := s.head, p.q
	if q.After != nil {
		if compareTo(r.at, s.file.rel, r.seq, *q.After) <= 0 || (!q.Since.IsZero() && r.at.Before(q.Since)) {
			return
		}
		if !q.Until.IsZero() && r.at.After(q.Until) {
			p.done = true
			return
		}
	} else {
		if (q.Before != nil && compareTo(r.at, s.file.rel, r.seq, *q.Before) >= 0) || (!q.Until.IsZero() && r.at.After(q.Until)) {
			return
		}
		if !q.Since.IsZero() && r.at.Before(q.Since) {
			p.done = true
			return
		}
	}
	if p.m.re != nil && !p.m.re.Match(r.text) { // spares the string conversion for most lines of a search
		return
	}
	l := Line{At: r.at, App: s.file.app, Service: s.file.service, Container: s.file.c12, Stream: string(r.stream), Text: Truncate(string(r.text))}
	if !p.m.Match(l) {
		return
	}
	cost := len(l.Text) + len(l.App) + len(l.Service) + lineJSONOverhead
	if len(p.lines) > 0 && p.bytes+cost > MaxPageBytes {
		p.full, p.done = true, true
		return
	}
	p.lines = append(p.lines, l)
	p.bytes += cost
	p.last = Cursor{At: r.at, File: s.file.rel, Seq: r.seq}
	if len(p.lines) == p.limit {
		p.full, p.done = true, true
	}
}

type rec struct {
	at     time.Time
	seq    int
	stream byte
	text   []byte // valid until the reader's next call
}

type lineReader interface {
	next() (rec, bool, error)
}

type source struct {
	lineReader
	f    *os.File
	file logFile
	head rec
	ok   bool
}

func (s *source) advance() (err error) {
	s.head, s.ok, err = s.next()
	return err
}

// parseRaw is ParseLine on bytes, so the text is copied only for lines that pass the bounds.
func parseRaw(b []byte) (at time.Time, stream byte, text []byte, ok bool) {
	i := bytes.IndexByte(b, ' ')
	if i < 0 || len(b) < i+3 || (b[i+1] != 'o' && b[i+1] != 'e') || b[i+2] != ' ' {
		return time.Time{}, 0, nil, false
	}
	at, err := time.Parse(time.RFC3339Nano, string(b[:i]))
	if err != nil {
		return time.Time{}, 0, nil, false
	}
	return at, b[i+1], b[i+3:], true
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

type fwdReader struct {
	r   *bufio.Reader
	seq int
}

func (s *fwdReader) next() (rec, bool, error) {
	for {
		b, err := s.r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			long := append([]byte(nil), b...)
			for err == bufio.ErrBufferFull {
				b, err = s.r.ReadSlice('\n')
				long = append(long, b[:min(len(b), max(maxReadLine-len(long), 0))]...)
			}
			b = long
		}
		if err == io.EOF {
			return rec{}, false, nil // a last line without '\n' is still being written
		}
		if err != nil {
			return rec{}, false, err
		}
		seq := s.seq
		s.seq++
		if at, stream, text, ok := parseRaw(bytes.TrimSuffix(b, []byte{'\n'})); ok {
			return rec{at, seq, stream, text}, true, nil
		}
	}
}

// revReader returns a file's complete lines last to first, reading 64 KiB chunks from the end.
type revReader struct {
	ctx context.Context
	f   *os.File
	off int64  // file bytes [0, off) are not read yet
	buf []byte // file bytes from off: whole lines, each ending with '\n', not returned yet
	seq int    // index of buf's last line
}

func newRevReader(ctx context.Context, f *os.File) (*revReader, error) {
	// Seq counts from the start of the file, so count the lines first; bytes after the last '\n' are still being written.
	r := bufio.NewReaderSize(ctxReader{ctx, f}, readChunk)
	var n int
	var pos, end int64
	for {
		b, err := r.ReadSlice('\n')
		pos += int64(len(b))
		if len(b) > 0 && b[len(b)-1] == '\n' {
			n++
			end = pos
		}
		if err == io.EOF {
			break
		}
		if err != nil && err != bufio.ErrBufferFull {
			return nil, err
		}
	}
	return &revReader{ctx: ctx, f: f, off: end, seq: n - 1}, nil
}

func (s *revReader) next() (rec, bool, error) {
	for {
		if len(s.buf) == 0 && s.off == 0 {
			return rec{}, false, nil
		}
		j := bytes.LastIndexByte(s.buf[:max(len(s.buf)-1, 0)], '\n')
		if j < 0 && s.off > 0 {
			if err := s.fill(); err != nil {
				return rec{}, false, err
			}
			continue
		}
		line := s.buf[j+1 : len(s.buf)-1]
		s.buf = s.buf[:j+1]
		seq := s.seq
		s.seq--
		if at, stream, text, ok := parseRaw(line); ok {
			return rec{at, seq, stream, text}, true, nil
		}
	}
}

// fill prepends the previous chunk into a new array, so text handed out earlier stays valid.
func (s *revReader) fill() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	keep := s.buf // the tail of one line, with its '\n'
	if len(keep) > maxReadLine {
		keep = append(keep[:maxReadLine:maxReadLine], '\n')
	}
	n := min(s.off, readChunk)
	b := make([]byte, int(n)+len(keep))
	if _, err := s.f.ReadAt(b[:n], s.off-n); err != nil {
		return err
	}
	copy(b[n:], keep)
	s.off -= n
	s.buf = b
	return nil
}

type logFile struct {
	rel, app, service, day, c12 string // rel uses forward slashes
	size                        int64
}

// listLogs returns <app>/<service>/<day>.<c12>.log files sorted by app, service, name; nil sets = all.
func listLogs(root string, apps, services map[string]bool) ([]logFile, error) {
	var out []logFile
	appDirs, err := readDir(root)
	if err != nil {
		return nil, err
	}
	for _, a := range appDirs {
		if !a.IsDir() || a.Name() == "_events" || (apps != nil && !apps[a.Name()]) {
			continue
		}
		svcDirs, err := readDir(filepath.Join(root, a.Name()))
		if err != nil {
			return nil, err
		}
		for _, sv := range svcDirs {
			if !sv.IsDir() || (services != nil && !services[sv.Name()]) {
				continue
			}
			names, err := readDir(filepath.Join(root, a.Name(), sv.Name()))
			if err != nil {
				return nil, err
			}
			for _, de := range names {
				n := de.Name()
				if de.IsDir() || !strings.HasSuffix(n, ".log") || len(n) <= len("2006-01-02..log") || n[10] != '.' {
					continue
				}
				if _, err := time.Parse("2006-01-02", n[:10]); err != nil {
					continue
				}
				info, err := de.Info()
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				out = append(out, logFile{rel: a.Name() + "/" + sv.Name() + "/" + n, app: a.Name(), service: sv.Name(),
					day: n[:10], c12: n[11 : len(n)-4], size: info.Size()})
			}
		}
	}
	return out, nil
}

// readDir treats a missing directory as empty: the purge removes the directories it empties.
func readDir(name string) ([]os.DirEntry, error) {
	des, err := os.ReadDir(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return des, err
}

type ServiceSummary struct {
	Service   string    `json:"service"`
	Container string    `json:"container"`
	Running   bool      `json:"running"` // set by the caller (service layer)
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
	SizeBytes int64     `json:"size_bytes"`
}

type AppSummary struct {
	App      string           `json:"app"`
	Services []ServiceSummary `json:"services"`
}

func ListApps(root string) ([]AppSummary, error) {
	files, err := listLogs(root, nil, nil)
	if err != nil {
		return nil, err
	}
	out := []AppSummary{}
	for i := 0; i < len(files); {
		j := i
		for j < len(files) && files[j].app == files[i].app && files[j].service == files[i].service {
			j++
		}
		run := files[i:j] // oldest day first: names start with the day
		newest := run[len(run)-1]
		s := ServiceSummary{Service: newest.service, Container: newest.c12,
			FirstAt: firstLineAt(filepath.Join(root, filepath.FromSlash(run[0].rel))),
			LastAt:  lastLineAt(filepath.Join(root, filepath.FromSlash(newest.rel)))}
		for _, f := range run {
			s.SizeBytes += f.size
		}
		if len(out) == 0 || out[len(out)-1].App != newest.app {
			out = append(out, AppSummary{App: newest.app})
		}
		out[len(out)-1].Services = append(out[len(out)-1].Services, s)
		i = j
	}
	return out, nil
}

func firstLineAt(name string) time.Time {
	f, err := os.Open(name)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	r, ok, _ := (&fwdReader{r: bufio.NewReaderSize(f, readChunk)}).next()
	if !ok {
		return time.Time{}
	}
	return r.at
}
