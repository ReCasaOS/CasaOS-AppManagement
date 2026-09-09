package common

import (
	"context"
	"maps"
	"sync"
)

type (
	keyTypeProperties       int
	keyTypeInterpolationMap int
)

const (
	keyProperties       keyTypeProperties       = iota
	keyInterpolationMap keyTypeInterpolationMap = iota
)

// Returns a new context with the given properties for events.
func WithProperties(ctx context.Context, properties map[string]string) context.Context {
	return withMap(ctx, keyProperties, properties)
}

// propertiesLock guards every event-property map in flight. Events are published
// from goroutines -- `go PublishEventWrapper(ctx, ...)` -- that range over the map
// while the request that started them is still adding to it: `app:updated` is
// written at the very end of a recreate, long after the first event goroutine
// started. A concurrent map read and write is a runtime fatal, not a panic a
// handler can recover from: it takes the whole daemon down.
//
// ponytail: one lock for all of them. Per-context locks if event volume ever makes
// this contended, which a handful of events per user action will not.
var propertiesLock sync.RWMutex

// PropertiesFromContext returns a copy of the event properties carried by ctx, safe
// to read while another goroutine is still adding to them. Writing to the copy
// changes nothing the events will carry -- SetProperties is what does that.
func PropertiesFromContext(ctx context.Context) map[string]string {
	propertiesLock.RLock()
	defer propertiesLock.RUnlock()

	return maps.Clone(mapFromContext(ctx, keyProperties))
}

// SetProperties adds to the event properties carried by ctx, so that every event
// published from it -- including the ones already in flight -- carries them.
//
// A context built without WithProperties has nowhere to put them, and they are
// dropped: it is a context that publishes no events of its own.
func SetProperties(ctx context.Context, properties map[string]string) {
	propertiesLock.Lock()
	defer propertiesLock.Unlock()

	carried := mapFromContext(ctx, keyProperties)
	if carried == nil {
		return
	}

	maps.Copy(carried, properties)
}

func withMap[T any](ctx context.Context, key T, value map[string]string) context.Context {
	return context.WithValue(ctx, key, value)
}

func mapFromContext[T any](ctx context.Context, key T) map[string]string {
	value := ctx.Value(key)
	if value == nil {
		return nil
	}

	if properties, ok := value.(map[string]string); ok {
		return properties
	}

	return nil
}
