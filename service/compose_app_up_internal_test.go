package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// fakeCompose stands in for the two halves of compose's Up. Start records that it was
// reached at all, which is the property under test.
type fakeCompose struct {
	createErr    error
	startErr     error
	startCalled  bool
	startOptions api.StartOptions
}

func (f *fakeCompose) Create(context.Context, *types.Project, api.CreateOptions) error {
	return f.createErr
}

func (f *fakeCompose) Start(_ context.Context, _ string, options api.StartOptions) error {
	f.startCalled = true
	f.startOptions = options
	return f.startErr
}

// Everything compose does before convergence -- resolving an image that does not exist,
// creating a network or a volume, rejecting a duplicate container_name -- fails with the
// PREVIOUS definition's containers still running and healthy. If such a failure reached
// the settled check, those containers would answer for a definition they were never
// created from: the apply would be recorded as "started but unconfirmed", the new
// compose file kept, and the backup dropped over an app that cannot start from it.
//
// So the create must not go through waitForApp at all, and its error must be an ordinary
// failed apply.
func TestUpDoesNotJudgeAFailureThatNeverReachedTheContainers(t *testing.T) {
	logger.LogInitConsoleOnly()
	setUpWaitTimeout(t, shortestUpWaitTimeout)

	app := &ComposeApp{Name: "nextcloud"}
	missing := errors.New("manifest for nextcloud:38 not found")
	compose := &fakeCompose{createErr: missing}

	err := app.up(context.Background(), compose, false)
	if !errors.Is(err, missing) {
		t.Fatalf("compose's own error must reach the caller, got %v", err)
	}
	if errors.Is(err, errUpNotConfirmed) || keepNewDefinition(err) {
		t.Fatal("a create that failed is a failed apply: the new compose file must be rolled back")
	}
	if compose.startCalled {
		t.Fatal("nothing may be started once the create failed")
	}
}

// And the other half still holds: once the containers exist they are the new definition,
// so a start that will not confirm is judged by them.
func TestUpStartsWhatItCreated(t *testing.T) {
	logger.LogInitConsoleOnly()
	setUpWaitTimeout(t, shortestUpWaitTimeout)

	app := &ComposeApp{Name: "nextcloud"}
	compose := &fakeCompose{}

	if err := app.up(context.Background(), compose, false); err != nil {
		t.Fatalf("an app that came up must not be an error: %v", err)
	}
	if !compose.startCalled {
		t.Fatal("a created app must then be started")
	}
}

// compose rebuilds a project it is not given from the containers' labels, and labels carry no
// post_start or pre_start hook: a container CasaOS created never ran them. The start is given
// the app's own project, which is where the hooks are.
func TestUpStartsWithTheProjectItsHooksComeFrom(t *testing.T) {
	logger.LogInitConsoleOnly()
	setUpWaitTimeout(t, shortestUpWaitTimeout)

	app := &ComposeApp{Name: "jarvis", Services: types.Services{
		"web": {Name: "web", PostStart: []types.ServiceHook{{Command: types.ShellCommand{"touch", "/tmp/started"}}}},
	}}
	compose := &fakeCompose{}

	if err := app.up(context.Background(), compose, false); err != nil {
		t.Fatalf("an app that came up must not be an error: %v", err)
	}
	project := compose.startOptions.Project
	if project == nil || len(project.Services["web"].PostStart) != 1 {
		t.Fatal("the start must be given the app's project, which is where its hooks are")
	}
}
