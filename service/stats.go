package service

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// StatsDir is where the sampler keeps its history and its alerts.
var StatsDir = "/var/lib/casaos/stats/app_management"

var (
	statsSampler atomic.Pointer[dockerstats.Sampler]

	statsMu     sync.Mutex // StartStats, StopStats and SetStats
	statsCancel context.CancelFunc
	statsDone   chan struct{}
	statsSource *dockerstats.DockerSource
	statsBus    *alertPublisher
)

// Stats is the running sampler, nil when it is not running.
func Stats() *dockerstats.Sampler { return statsSampler.Load() }

// SetStats puts a sampler in place of the running one, for tests.
func SetStats(s *dockerstats.Sampler) {
	statsMu.Lock()
	defer statsMu.Unlock()
	statsSampler.Store(s)
}

// StartStats opens the sampler and runs it until ctx ends or StopStats; it does nothing
// when the sampler is already running. An error leaves it not running: the stats routes
// answer 503 and the rest of the service is unaffected.
func StartStats(ctx context.Context) error {
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsCancel != nil {
		return nil
	}
	source, err := dockerstats.NewDockerSource()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	bus := startAlertPublisher(alertQueueSize)
	sampler, err := dockerstats.New(dockerstats.Config{
		Dir:      StatsDir,
		Source:   source,
		Clock:    stats.SystemClock{},
		Interval: 5 * time.Second,
		Emit: func(e stats.Event, who dockerstats.Identity, minutes int) {
			emitAlert(ctx, bus, e, who, minutes)
		},
	})
	if err != nil {
		cancel()
		bus.stop()
		_ = source.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sampler.Run(ctx)
	}()
	statsCancel, statsDone, statsSource, statsBus = cancel, done, source, bus
	statsSampler.Store(sampler)
	return nil
}

// StopStats stops the sampler and closes its files; it does nothing when the sampler is
// not running.
func StopStats() {
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsCancel == nil {
		return
	}
	statsCancel()
	<-statsDone
	if s := statsSampler.Swap(nil); s != nil {
		_ = s.Close()
	}
	statsBus.stop() // what is queued is still published, in order
	_ = statsSource.Close()
	statsCancel, statsDone, statsSource, statsBus = nil, nil, nil, nil
}

// alertQueueSize bounds the events waiting for the bus. Full means the bus has been
// stuck for longer than any burst of alerts: the NEWEST event is dropped and logged, so
// what is already queued keeps its order.
const alertQueueSize = 64

// publishAlert sends one event and returns when the bus has answered; replaced by tests.
var publishAlert = PublishEventWrapper

type alertJob struct {
	ctx        context.Context
	eventType  message_bus.EventType
	properties map[string]string
}

// alertPublisher is one goroutine fed by a bounded channel, so that a firing and the
// resolved that follows it reach the bus in the order the sampler emitted them, and the
// sampler never waits on the bus.
type alertPublisher struct {
	mu     sync.Mutex
	closed bool
	jobs   chan alertJob
	done   chan struct{}
}

func startAlertPublisher(size int) *alertPublisher {
	p := &alertPublisher{jobs: make(chan alertJob, size), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for j := range p.jobs {
			publishAlert(j.ctx, j.eventType, j.properties)
		}
	}()
	return p
}

// submit never blocks; false when the event was dropped (queue full or publisher stopped).
func (p *alertPublisher) submit(j alertJob) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	select {
	case p.jobs <- j:
		return true
	default:
		return false
	}
}

// stop publishes what is queued, then ends the goroutine; calling it again does nothing.
func (p *alertPublisher) stop() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.mu.Unlock()
	<-p.done
}

// emitAlert queues an alert of an item the sampler can name; an event without a kind
// and a name would mean nothing to its reader.
func emitAlert(ctx context.Context, bus *alertPublisher, e stats.Event, who dockerstats.Identity, minutes int) {
	if who.Kind == "" {
		logger.Error("a resource alert of an unidentified item is not published", zap.String("item", e.Item), zap.String("situation", e.Key))
		return
	}
	eventType, properties := resourceAlertEvent(e, who, minutes)
	if !bus.submit(alertJob{ctx, eventType, properties}) {
		logger.Error("a resource alert was dropped, the message bus queue is full or stopped", zap.String("item", e.Item), zap.String("situation", e.Key), zap.String("state", e.State))
	}
}

// alertName is the name an alert carries: "<app> / <service>" for a compose container
// ("web-2" for its second replica), the container's name for any other.
func alertName(who dockerstats.Identity) string {
	if who.Kind == "app-container" && who.App != "" {
		return who.App + " / " + who.Name
	}
	return who.Name
}

// resourceAlertEvent is the message bus event of an alert that fired or resolved. minutes
// is the rule's window.
func resourceAlertEvent(e stats.Event, who dockerstats.Identity, minutes int) (message_bus.EventType, map[string]string) {
	eventType := common.EventTypeResourceAlertResolved
	if e.State == "firing" {
		eventType = common.EventTypeResourceAlertFiring
	}
	properties := map[string]string{
		"item":      e.Item,
		"name":      alertName(who),
		"kind":      who.Kind,
		"situation": e.Key,
		"minutes":   strconv.Itoa(minutes),
		"since":     e.Since.UTC().Format(time.RFC3339),
		"source":    "app_management",
	}
	if e.State == "firing" && !math.IsNaN(e.Value) && !math.IsInf(e.Value, 0) {
		properties["value"] = strconv.FormatFloat(e.Value, 'f', 0, 64)
	}
	return eventType, properties
}
