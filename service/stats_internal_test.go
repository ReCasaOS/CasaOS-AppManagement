package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
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
