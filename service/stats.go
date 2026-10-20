package service

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
)

// StatsDir is where the sampler keeps its history and its alerts.
var StatsDir = "/var/lib/casaos/stats/app_management"

var (
	statsSampler atomic.Pointer[dockerstats.Sampler]

	statsMu     sync.Mutex // StartStats and StopStats
	statsCancel context.CancelFunc
	statsDone   chan struct{}
	statsSource *dockerstats.DockerSource
)

// Stats is the running sampler, nil when it is not running.
func Stats() *dockerstats.Sampler { return statsSampler.Load() }

// SetStats puts a sampler in place of the running one, for tests.
func SetStats(s *dockerstats.Sampler) { statsSampler.Store(s) }

// StartStats opens the sampler and runs it until ctx ends or StopStats. An error leaves
// it not running: the stats routes answer 503 and the rest of the service is unaffected.
func StartStats(ctx context.Context) error {
	statsMu.Lock()
	defer statsMu.Unlock()
	source, err := dockerstats.NewDockerSource()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	sampler, err := dockerstats.New(dockerstats.Config{
		Dir:      StatsDir,
		Source:   source,
		Clock:    stats.SystemClock{},
		Interval: 5 * time.Second,
		// never held up by the bus, as the Docker watch
		Emit: func(e stats.Event, who dockerstats.Identity, minutes int) {
			eventType, properties := resourceAlertEvent(e, who, minutes)
			go PublishEventWrapper(ctx, eventType, properties)
		},
	})
	if err != nil {
		cancel()
		_ = source.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sampler.Run(ctx)
	}()
	statsCancel, statsDone, statsSource = cancel, done, source
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
	_ = statsSource.Close()
	statsCancel, statsDone, statsSource = nil, nil, nil
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
		"name":      who.Name,
		"kind":      who.Kind,
		"situation": e.Key,
		"minutes":   strconv.Itoa(minutes),
		"since":     e.Since.UTC().Format(time.RFC3339),
		"source":    "app_management",
	}
	if e.State == "firing" {
		properties["value"] = strconv.FormatFloat(e.Value, 'f', 0, 64)
	}
	return eventType, properties
}
