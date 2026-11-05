package logstore

import (
	"context"
	"os"
	"sync"
	"time"
)

// Store is the single entry point of the package: it owns the writers, the hub and the root directory.
type Store struct {
	Root string
	// OnError, if set before use, receives every writer's flush error (nil = ignored).
	OnError func(error)

	ctx     context.Context
	hub     *Hub
	mu      sync.Mutex
	writers map[string]*Writer
}

// Open creates root if needed. ctx bounds the writers' background flush loops.
func Open(ctx context.Context, root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root, ctx: ctx, hub: NewHub(), writers: map[string]*Writer{}}, nil
}

func containerKey(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Writer returns the container's writer, creating and starting it on first use.
func (s *Store) Writer(app, service, container string) *Writer {
	key := containerKey(container)
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.writers[key]; ok {
		return w
	}
	w := NewWriter(s.Root, app, service, key, func(l Line) { s.hub.Publish(Message{T: "line", Line: &l}) })
	w.OnError = func(err error) {
		if s.OnError != nil {
			s.OnError(err)
		}
	}
	s.writers[key] = w
	go w.Run(s.ctx)
	return w
}

// CloseWriter flushes, closes and forgets the container's writer; unknown ids are a no-op.
func (s *Store) CloseWriter(container string) error {
	key := containerKey(container)
	s.mu.Lock()
	w, ok := s.writers[key]
	delete(s.writers, key)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return w.Close()
}

// Record appends the event to the journal, then publishes it; nothing is published on error.
func (s *Store) Record(e Event) error {
	if err := AppendEvent(s.Root, e); err != nil {
		return err
	}
	s.hub.Publish(Message{T: "event", Event: &e})
	return nil
}

func (s *Store) Read(ctx context.Context, q Query) (Page, error) { return Read(ctx, s.Root, q) }
func (s *Store) Subscribe(m *Matcher) (<-chan Message, func())   { return s.hub.Subscribe(m) }
func (s *Store) Apps() ([]AppSummary, error)                     { return ListApps(s.Root) }
func (s *Store) Events(q EventQuery) ([]Event, error)            { return ReadEvents(s.Root, q) }

func (s *Store) Purge(now time.Time, retentionDays int, maxBytes int64) (int64, error) {
	return Purge(s.Root, now, retentionDays, maxBytes)
}

func (s *Store) Usage() (int64, time.Time, error) { return Usage(s.Root) }

// Close flushes and closes every writer and returns the first error.
func (s *Store) Close() error {
	s.mu.Lock()
	ws := s.writers
	s.writers = map[string]*Writer{}
	s.mu.Unlock()
	var first error
	for _, w := range ws {
		if err := w.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
