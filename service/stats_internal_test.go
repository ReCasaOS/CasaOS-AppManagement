package service

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func TestAFiringAlertBecomesAResourceAlertFiringEvent(t *testing.T) {
	e := stats.Event{
		Item: "app:jarvis:web:1", Key: "cpu_high", State: "firing", Value: 93.4,
		Since: time.Date(2026, 10, 20, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60)),
	}
	eventType, properties := resourceAlertEvent(e, dockerstats.Identity{Name: "web", Kind: "app-container"}, 10)

	assert.Equal(t, eventType.Name, "resource:alert-firing")
	assert.Equal(t, eventType.SourceID, "app-management")
	assert.DeepEqual(t, properties, map[string]string{
		"item":      "app:jarvis:web:1",
		"name":      "web",
		"kind":      "app-container",
		"situation": "cpu_high",
		"value":     "93",
		"minutes":   "10",
		"since":     "2026-10-20T10:00:00Z",
		"source":    "app_management",
	})
}

func TestAResolvedAlertCarriesNoValue(t *testing.T) {
	e := stats.Event{
		Item: "container:db", Key: "mem_high", State: "resolved", Value: 12,
		Since: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
	}
	eventType, properties := resourceAlertEvent(e, dockerstats.Identity{Name: "db", Kind: "container"}, 5)

	assert.Equal(t, eventType.Name, "resource:alert-resolved")
	_, has := properties["value"]
	assert.Assert(t, !has)
	assert.Equal(t, properties["minutes"], "5")
	assert.Equal(t, properties["situation"], "mem_high")
}

func TestStartStatsWithAnUnusableDirLeavesStatsNil(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	assert.NilError(t, os.WriteFile(file, nil, 0o600))
	old := StatsDir
	StatsDir = filepath.Join(file, "stats")
	t.Cleanup(func() { StatsDir = old; StopStats() })

	assert.Assert(t, StartStats(context.Background()) != nil)
	assert.Assert(t, Stats() == nil)
}

func TestStopStatsIsSafeTwiceAndNeverStarted(t *testing.T) {
	StopStats()
	StopStats()
	assert.Assert(t, Stats() == nil)
}

func TestStartThenStopStats(t *testing.T) {
	old := StatsDir
	StatsDir = t.TempDir()
	t.Cleanup(func() { StatsDir = old; StopStats() })

	assert.NilError(t, StartStats(context.Background()))
	assert.Assert(t, Stats() != nil)
	StopStats()
	assert.Assert(t, Stats() == nil)
}

func TestAnAlertOfAnUnidentifiedItemIsNotPublished(t *testing.T) {
	logger.LogInitConsoleOnly()
	var published []map[string]string
	old := publishAlert
	publishAlert = func(_ context.Context, _ message_bus.EventType, properties map[string]string) {
		published = append(published, properties)
	}
	t.Cleanup(func() { publishAlert = old })

	e := stats.Event{Item: "weird", Key: "cpu_high", State: "firing", Value: 95, Since: time.Unix(0, 0)}
	emitAlert(context.Background(), e, dockerstats.Identity{ID: "weird"}, 10)
	assert.Equal(t, len(published), 0)

	emitAlert(context.Background(), e, dockerstats.Identity{ID: "container:db", Name: "db", Kind: "container"}, 10)
	assert.Equal(t, len(published), 1)
	assert.Equal(t, published[0]["kind"], "container")
}

func TestANonFiniteValueIsLeftOut(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		e := stats.Event{Item: "container:db", Key: "cpu_high", State: "firing", Value: v, Since: time.Unix(0, 0)}
		_, properties := resourceAlertEvent(e, dockerstats.Identity{Name: "db", Kind: "container"}, 10)
		_, has := properties["value"]
		assert.Assert(t, !has, "%v gave value %q", v, properties["value"])
	}
}

func TestStartStatsTwiceKeepsTheRunningSampler(t *testing.T) {
	old := StatsDir
	StatsDir = t.TempDir()
	t.Cleanup(func() { StatsDir = old; StopStats() })

	assert.NilError(t, StartStats(context.Background()))
	first := Stats()
	assert.NilError(t, StartStats(context.Background()))
	assert.Assert(t, Stats() == first, "a second start replaced the sampler")
	StopStats()
	assert.Assert(t, Stats() == nil)
}

func TestSetStatsWaitsForStartAndStop(t *testing.T) {
	statsMu.Lock()
	done := make(chan struct{})
	go func() { SetStats(nil); close(done) }()
	select {
	case <-done:
		t.Fatal("SetStats did not take the lock")
	case <-time.After(50 * time.Millisecond):
	}
	statsMu.Unlock()
	<-done
}
