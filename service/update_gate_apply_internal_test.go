package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/bluele/gcache"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"gotest.tools/v3/assert"
)

// The app an update gate test works on: `app:latest`, whose tag the update's pull moved to
// sha256:new while its container still runs sha256:old.
const (
	gatedCompose = "name: web\nservices:\n  app:\n    image: app:latest\n"
	// the update's compose file: VERSION tells its containers from the running ones
	gatedNext = "name: web\nservices:\n  app:\n    image: app:latest\n    environment:\n      VERSION: \"2\"\n"
)

// createRecorder is compose's Up as pullAndApply drives it, recording each definition created.
type createRecorder struct {
	api.Compose
	created  []*types.Project
	started  int
	failNext error // what a create of the update's definition answers
}

func (c *createRecorder) Create(_ context.Context, project *types.Project, _ api.CreateOptions) error {
	c.created = append(c.created, project)
	if _, next := project.Services["app"].Environment["VERSION"]; next {
		return c.failNext
	}
	return nil
}

func (c *createRecorder) Start(context.Context, string, api.StartOptions) error {
	c.started++
	return nil
}

// createdNext reports whether the update's own definition was ever created.
func (c *createRecorder) createdNext() bool {
	for _, project := range c.created {
		if _, ok := project.Services["app"].Environment["VERSION"]; ok {
			return true
		}
	}
	return false
}

type gateRig struct {
	app         *ComposeApp
	composeFile string
	compose     *createRecorder
	daemon      *fakeTagger
	mu          sync.Mutex
	asked       int            // times checkCandidates was called
	gated       [2]*ComposeApp // the running app and the next one, as the gate was last given them
}

func (r *gateRig) checks() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked
}

func blockedByTheScan() *UpdateBlockedError {
	return &UpdateBlockedError{Images: []BlockedImage{{
		Service: "app", Image: "app:latest", RepoDigests: []string{"app@sha256:bb"}, ID: "sha256:new",
		Current: 1, Candidate: 3, Added: []codegen.BlockedFinding{{Id: "CVE-1", Package: "openssl", Fixed: "3.0.2", Title: "t", Url: "u"}},
	}}}
}

// newGateRig puts an app on disk and gives pullAndApply a daemon of its own and a gate that
// answers verdict.
func newGateRig(t *testing.T, verdict error) *gateRig {
	t.Helper()
	logger.LogInitConsoleOnly()
	setRuntimePathForTest(t)
	// the pull reaches no daemon: it fails, which is published, not returned
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile, []byte(gatedCompose), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("OLD=1\n"), 0o600))
	app, err := LoadComposeAppFromConfigFile("web", composeFile)
	assert.NilError(t, err)

	r := &gateRig{
		app: app, composeFile: composeFile, compose: &createRecorder{},
		daemon: &fakeTagger{
			tags:       map[string]string{"app:latest": "sha256:new"},
			containers: []container.Summary{aContainer("web", "app", "sha256:old")},
		},
	}

	wasDocker, wasCheck := applyDocker, checkCandidates
	t.Cleanup(func() { applyDocker, checkCandidates = wasDocker, wasCheck })
	applyDocker = func() (api.Compose, client.APIClient, error) { return r.compose, r.daemon, nil }
	checkCandidates = func(_ context.Context, _ imageTagger, running, next *ComposeApp) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.asked++
		r.gated = [2]*ComposeApp{running, next}
		if verdict == nil {
			return nil
		}
		return verdict
	}

	return r
}

// withUpdateService is the service an update reports to, and where its answers are kept.
func withUpdateService(t *testing.T) {
	t.Helper()
	wasService, wasPath := MyService, imageUpdateStatePath
	t.Cleanup(func() { MyService, imageUpdateStatePath = wasService, wasPath })
	MyService = &store{appStoreManagement: &AppStoreManagement{isAppUpgradable: gcache.New(10).LRU().Build()}}
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")
}

type busEvent struct {
	name       string
	properties map[string]string
}

// hearUpdate catches what an update publishes, the properties its context carries included.
// Some are published from goroutines, hence the lock.
func hearUpdate(t *testing.T) func(want ...string) []busEvent {
	t.Helper()
	var mu sync.Mutex
	got := []busEvent{}
	was := publishEvent
	t.Cleanup(func() { publishEvent = was })
	publishEvent = func(ctx context.Context, event message_bus.EventType, properties map[string]string) {
		all := map[string]string{}
		for k, v := range common.PropertiesFromContext(ctx) {
			all[k] = v
		}
		for k, v := range properties {
			all[k] = v
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, busEvent{event.Name, all})
	}

	// waits until every name wanted was heard, and returns what was
	return func(want ...string) []busEvent {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			heard := slices.Clone(got)
			mu.Unlock()
			missing := false
			for _, name := range want {
				missing = missing || !slices.ContainsFunc(heard, func(e busEvent) bool { return e.name == name })
			}
			if !missing {
				return heard
			}
			if time.Now().After(deadline) {
				t.Fatalf("never heard all of %v, heard %v", want, heard)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// untilIdle waits for the operation running on app, started in the background, to end.
func untilIdle(t *testing.T, app string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if end, err := Begin(app, "test"); err == nil {
			end()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the operation never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func updateEventNames(events []busEvent) []string {
	names := []string{}
	for _, e := range events {
		if e.name == common.EventTypeAppUpdateBegin.Name || e.name == common.EventTypeAppUpdateEnd.Name ||
			e.name == common.EventTypeAppUpdateError.Name || e.name == common.EventTypeAppUpdateBlocked.Name {
			names = append(names, e.name)
		}
	}
	slices.Sort(names)
	return names
}

// A blocked update ends where it started: the files it wrote are put back, the tag its pull
// moved points at the image the app runs again, the image it pulled is removed, and the
// update's own containers are never created.
func TestBlockedUpdatePutsEverythingBack(t *testing.T) {
	r := newGateRig(t, blockedByTheScan())

	newEnv := []byte("NEW=1\n")
	err := r.app.pullAndApply(context.Background(), []byte(gatedNext), &newEnv, true)

	var blocked *UpdateBlockedError
	assert.Assert(t, errors.As(err, &blocked), "%v", err)
	assert.Equal(t, r.checks(), 1)
	// the app as it runs, then the one the update pulled
	assert.Assert(t, r.gated[0] == r.app)
	_, next := r.gated[1].Services["app"].Environment["VERSION"]
	assert.Assert(t, next, "the gate was not given the update's definition")

	compose, err := os.ReadFile(r.composeFile)
	assert.NilError(t, err)
	assert.Equal(t, string(compose), gatedCompose)
	env, err := os.ReadFile(r.app.EnvFile())
	assert.NilError(t, err)
	assert.Equal(t, string(env), "OLD=1\n")

	// the tag first, then the image, which nothing names any more
	assert.DeepEqual(t, r.daemon.calls, []string{"tag app:latest sha256:old", "remove sha256:new"})
	assert.Equal(t, r.daemon.tags["app:latest"], "sha256:old")
	// never forced: Docker keeps an image a container or another tag holds
	assert.DeepEqual(t, r.daemon.removed, []client.ImageRemoveOptions{{}})
	// nothing was recreated before the gate, so nothing is brought back: no create, no start
	// of any definition, old or new
	assert.Equal(t, len(r.compose.created), 0)
	assert.Equal(t, r.compose.started, 0)
}

// Any other failure still brings the old app back up from the files put back.
func TestFailedUpdateStillRestartsTheOldApp(t *testing.T) {
	r := newGateRig(t, nil)
	r.compose.failNext = errors.New("manifest for app:latest not found")

	err := r.app.pullAndApply(context.Background(), []byte(gatedNext), nil, true)
	assert.ErrorContains(t, err, "manifest")
	assert.Equal(t, r.checks(), 1)

	compose, err := os.ReadFile(r.composeFile)
	assert.NilError(t, err)
	assert.Equal(t, string(compose), gatedCompose)
	// the update's definition, refused, then the old one
	assert.Equal(t, len(r.compose.created), 2)
	_, next := r.compose.created[0].Services["app"].Environment["VERSION"]
	assert.Assert(t, next)
	_, next = r.compose.created[1].Services["app"].Environment["VERSION"]
	assert.Assert(t, !next, "the old definition was not brought back")
	assert.Equal(t, r.compose.started, 1)
	assert.Equal(t, len(r.daemon.removed), 0)
}

// A blocked update is not an error: the owner is told it was blocked, and the update ends.
func TestBlockedUpdateIsNotAnError(t *testing.T) {
	r := newGateRig(t, blockedByTheScan())
	withUpdateService(t)
	heard := hearUpdate(t)

	err := r.app.UpdateNow(common.WithProperties(context.Background(), map[string]string{}))
	var blocked *UpdateBlockedError
	assert.Assert(t, errors.As(err, &blocked), "%v", err)

	events := heard(common.EventTypeAppUpdateBegin.Name, common.EventTypeAppUpdateBlocked.Name, common.EventTypeAppUpdateEnd.Name)
	assert.DeepEqual(t, updateEventNames(events), []string{"app:update-begin", "app:update-blocked", "app:update-end"})

	i := slices.IndexFunc(events, func(e busEvent) bool { return e.name == common.EventTypeAppUpdateBlocked.Name })
	properties := events[i].properties
	assert.Equal(t, properties[common.PropertyTypeMessage.Name], blockedByTheScan().Error())
	assert.Equal(t, properties[common.PropertyTypeAppName.Name], "web")
	var images []BlockedImage
	assert.NilError(t, json.Unmarshal([]byte(properties[common.PropertyTypeAppUpdateBlockedImages.Name]), &images))
	assert.DeepEqual(t, images, blockedByTheScan().Images)

	end := events[slices.IndexFunc(events, func(e busEvent) bool { return e.name == common.EventTypeAppUpdateEnd.Name })]
	assert.Equal(t, end.properties[common.PropertyTypeAppUpdated.Name], "", "a blocked update did not replace the app")
}

// Saving an app's settings recreates it from the images it has: there is nothing to scan.
func TestApplyOfSettingsIsNeverGated(t *testing.T) {
	r := newGateRig(t, blockedByTheScan())

	assert.NilError(t, r.app.Apply(context.Background(), []byte(gatedNext)))
	untilIdle(t, "web")

	assert.Equal(t, r.checks(), 0)
	assert.Assert(t, r.compose.createdNext(), "the settings were not applied")
	assert.Equal(t, len(r.daemon.removed), 0)
}

// The owner's update past the scan never asks it; the same update without the flag does.
func TestSkipScanBypassesTheGate(t *testing.T) {
	r := newGateRig(t, blockedByTheScan())
	withUpdateService(t)
	heard := hearUpdate(t)

	assert.NilError(t, r.app.Update(common.WithProperties(context.Background(), map[string]string{}), true))
	untilIdle(t, "web")
	events := heard(common.EventTypeAppUpdateEnd.Name)
	assert.Equal(t, r.checks(), 0)
	assert.DeepEqual(t, updateEventNames(events), []string{"app:update-begin", "app:update-end"})
	end := events[slices.IndexFunc(events, func(e busEvent) bool { return e.name == common.EventTypeAppUpdateEnd.Name })]
	assert.Equal(t, end.properties[common.PropertyTypeAppUpdated.Name], "true")
	assert.Equal(t, len(r.daemon.removed), 0)

	assert.NilError(t, r.app.Update(common.WithProperties(context.Background(), map[string]string{}), false))
	untilIdle(t, "web")
	heard(common.EventTypeAppUpdateBlocked.Name)
	assert.Equal(t, r.checks(), 1)
}

// The update of every app names the images an app's update was blocked on, and goes on.
func TestUpdateAllReportsBlocked(t *testing.T) {
	blocked := blockedByTheScan()
	state, message, images := updateOutcome(fmt.Errorf("update: %w", blocked))
	assert.Equal(t, state, codegen.ComposeAppUpdateRunAppStateBlocked)
	assert.Equal(t, message, blocked.Error())
	assert.DeepEqual(t, images, blocked.Images)

	state, message, images = updateOutcome(errors.New("the registry said no"))
	assert.Equal(t, state, codegen.ComposeAppUpdateRunAppStateFailed)
	assert.Equal(t, message, "the registry said no")
	assert.Assert(t, images == nil)

	updateAll.run = nil
	t.Cleanup(func() { updateAll.run = nil })
	_, err := StartUpdateAll([]string{"immich", "jellyfin"}, func(_ context.Context, id string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		if id == "immich" {
			return updateOutcome(blocked)
		}
		return updateOutcome(nil)
	})
	assert.NilError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	run := CurrentUpdateAllRun()
	for run.FinishedAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}
		time.Sleep(10 * time.Millisecond)
		run = CurrentUpdateAllRun()
	}

	immich := run.Apps[0]
	assert.Equal(t, immich.State, codegen.ComposeAppUpdateRunAppStateBlocked)
	assert.Equal(t, *immich.Message, blocked.Error())
	assert.Assert(t, immich.Blocked != nil)
	assert.DeepEqual(t, *immich.Blocked, []codegen.BlockedImage{{
		Service: "app", Image: "app:latest", RepoDigests: &[]string{"app@sha256:bb"}, Id: "sha256:new",
		Current: 1, Candidate: 3, Added: blocked.Images[0].Added,
	}})
	assert.Equal(t, run.Apps[1].State, codegen.ComposeAppUpdateRunAppStateUpdated)
	assert.Assert(t, run.Apps[1].Blocked == nil)
}
