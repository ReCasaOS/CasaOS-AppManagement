package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	message_bus "github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"gotest.tools/v3/assert"
)

type heard struct {
	name       string
	properties map[string]string
}

// hear catches what is published for the alerts (CasaOS#170).
func hear(t *testing.T) *[]heard {
	t.Helper()
	got := &[]heard{}
	was := publishEvent
	publishEvent = func(_ context.Context, event message_bus.EventType, properties map[string]string) {
		*got = append(*got, heard{event.Name, properties})
	}
	t.Cleanup(func() { publishEvent = was })
	return got
}

func TestTheNightSaysWhatItUpdatedAndWhatFailed(t *testing.T) {
	got := hear(t)
	left := "left alone: it failed two nights in a row; turn its switch off and on again to try once more"
	tellAutoUpdateNight(context.Background(), codegen.AutoUpdateRun{At: time.Now(), Apps: []codegen.ComposeAppUpdateRunApp{
		{Id: "jellyfin", State: codegen.ComposeAppUpdateRunAppStateUpdated},
		{Id: "plex", State: codegen.ComposeAppUpdateRunAppStateCurrent},
		{Id: "sonarr", State: codegen.ComposeAppUpdateRunAppStateFailed},
		{Id: "radarr", State: codegen.ComposeAppUpdateRunAppStateFailed, Message: &left},
		{Id: "immich", State: codegen.ComposeAppUpdateRunAppStateUpdated},
	}})

	assert.Equal(t, len(*got), 1)
	assert.Equal(t, (*got)[0].name, "auto-update:night-end")
	assert.Equal(t, (*got)[0].properties["auto-update:updated"], "jellyfin,immich")
	assert.Equal(t, (*got)[0].properties["auto-update:failed"], "sonarr") // radarr was told when it failed
}

func TestANightThatChangedNothingSaysNothing(t *testing.T) {
	got := hear(t)
	tellAutoUpdateNight(context.Background(), codegen.AutoUpdateRun{Apps: []codegen.ComposeAppUpdateRunApp{
		{Id: "plex", State: codegen.ComposeAppUpdateRunAppStateCurrent},
		{Id: "sonarr", State: codegen.ComposeAppUpdateRunAppStateQueued},
	}})
	assert.Equal(t, len(*got), 0)
}

// A newer version is told once: not again the next day, not again after a restart; a new one
// is told alone.
func TestANewerVersionIsToldOnce(t *testing.T) {
	got := hear(t)
	was := NewerTagsToldPath
	NewerTagsToldPath = filepath.Join(t.TempDir(), "told.json")
	t.Cleanup(func() { NewerTagsToldPath = was })

	pass := map[string][]codegen.NewerTag{"immich": {{Service: "db", Image: "postgres:16.2", Current: "16.2", Minor: utils.Ptr("16.4"), Major: utils.Ptr("17.0")}}}
	tellNewerTags(context.Background(), pass)
	assert.Equal(t, len(*got), 1)
	var told []toldTag
	assert.NilError(t, stdjson.Unmarshal([]byte((*got)[0].properties["newer-tags:found"]), &told))
	assert.Equal(t, len(told), 2)

	tellNewerTags(context.Background(), pass) // the next day, or after a restart
	assert.Equal(t, len(*got), 1)

	pass["immich"][0].Minor = utils.Ptr("16.5")
	tellNewerTags(context.Background(), pass)
	assert.Equal(t, len(*got), 2)
	assert.NilError(t, stdjson.Unmarshal([]byte((*got)[1].properties["newer-tags:found"]), &told))
	assert.DeepEqual(t, told, []toldTag{{App: "immich", Image: "postgres:16.2", Current: "16.2", Tag: "16.5", Level: "minor"}})
}

func TestTheWeeklyCleanupSaysWhatItFreedOrWhyNot(t *testing.T) {
	got := hear(t)
	daemon := weeklyBox(t)
	runWeeklyCleanup(context.Background(), daemon, nextSunday, nextSunday.Add(time.Minute))
	assert.Equal(t, len(*got), 1)
	assert.Equal(t, (*got)[0].name, "cleanup:end")
	assert.Equal(t, (*got)[0].properties["cleanup:count"], "2")
	assert.Equal(t, (*got)[0].properties["cleanup:size"], "542")

	appsProtection = func(context.Context) (cleanupProtection, error) {
		return cleanupProtection{}, errors.New("compose file of `odd`: yaml")
	}
	runWeeklyCleanup(context.Background(), daemon, nextSunday.AddDate(0, 0, 7), nextSunday.AddDate(0, 0, 7).Add(time.Minute))
	assert.Equal(t, (*got)[1].name, "cleanup:error")
	assert.Assert(t, (*got)[1].properties[common.PropertyTypeMessage.Name] != "")
}
