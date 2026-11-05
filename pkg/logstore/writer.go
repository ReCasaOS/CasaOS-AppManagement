package logstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const flushThreshold = 64 << 10

// Writer appends one container's lines to its per-day files through a buffer.
type Writer struct {
	root, app, service, container string // container is already cut to 12 chars
	onLine                        func(Line)

	mu     sync.Mutex
	buf    bytes.Buffer
	file   *os.File
	day    string // UTC day of buf and file, "2006-01-02"
	closed bool
}

// onLine runs synchronously inside Write and must stay cheap; nil is allowed.
func NewWriter(root, app, service, container string, onLine func(Line)) *Writer {
	if len(container) > 12 {
		container = container[:12]
	}
	return &Writer{root: root, app: app, service: service, container: container, onLine: onLine}
}

func (w *Writer) Write(at time.Time, stream, text string) error {
	text = Truncate(text)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("logstore: write on closed writer")
	}
	day := at.UTC().Format("2006-01-02")
	if day != w.day {
		if err := w.flushLocked(); err != nil {
			return err
		}
		w.closeFile()
		w.day = day
	}
	w.buf.Write(FormatLine(at, stream, text))
	var err error
	if w.buf.Len() >= flushThreshold {
		err = w.flushLocked()
	}
	if w.onLine != nil {
		w.onLine(Line{At: at, App: w.app, Service: w.service, Container: w.container, Stream: stream, Text: text})
	}
	return err
}

// Flush does not fsync: the 1 s crash window is accepted by the spec.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *Writer) flushLocked() error {
	if w.buf.Len() == 0 {
		return nil
	}
	if w.file == nil {
		day, _ := time.Parse("2006-01-02", w.day)
		name := DayFile(w.root, w.app, w.service, w.container, day)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		w.file = f
	}
	_, err := w.file.Write(w.buf.Bytes())
	w.buf.Reset() // drop even on error: retrying a failing disk would grow the buffer without bound
	return err
}

func (w *Writer) closeFile() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = w.Flush()
		case <-ctx.Done():
			_ = w.Flush()
			return
		}
	}
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.flushLocked()
	w.closeFile()
	w.closed = true
	return err
}

// LastTimestamp returns the time of the last parsable line of the container's newest day file.
func LastTimestamp(root, app, service, container string) (time.Time, bool) {
	if len(container) > 12 {
		container = container[:12]
	}
	files, err := filepath.Glob(path.Join(root, SafeName(app), SafeName(service), "*."+SafeName(container)+".log"))
	if err != nil || len(files) == 0 {
		return time.Time{}, false
	}
	sort.Strings(files)
	f, err := os.Open(files[len(files)-1])
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return time.Time{}, false
	}
	start := max(st.Size()-flushThreshold, 0)
	tail := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(tail, start); err != nil && !errors.Is(err, io.EOF) {
		return time.Time{}, false
	}
	lines := bytes.Split(tail, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if i == 0 && start > 0 {
			break // first chunk line is cut mid-line
		}
		if at, _, _, ok := ParseLine(lines[i]); ok {
			return at, true
		}
	}
	return time.Time{}, false
}
