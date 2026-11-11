package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
)

func appForGlobals(t *testing.T) *ComposeApp {
	t.Helper()
	logger.LogInitConsoleOnly()
	setUpWaitTimeout(t, shortestUpWaitTimeout)
	global := config.Global
	config.Global = map[string]string{"TZ": "Europe/Paris"}
	t.Cleanup(func() { config.Global = global })

	return &ComposeApp{Name: "demo", Services: types.Services{"web": {Name: "web"}}}
}

// A change of a global variable is not a start button: an app the owner stopped gets the new
// value, for the next time it is started, and stays stopped.
func TestAChangedGlobalDoesNotStartAStoppedApp(t *testing.T) {
	app, compose := appForGlobals(t), &fakeCompose{}

	if err := app.reapplyGlobals(context.Background(), compose, lists([2]string{"c1", "exited"}, [2]string{"c2", "created"})); err != nil {
		t.Fatal(err)
	}
	if compose.startCalled {
		t.Fatal("the stopped app was started")
	}
	if compose.created == nil || compose.created.Services["web"].Environment["TZ"] == nil || *compose.created.Services["web"].Environment["TZ"] != "Europe/Paris" {
		t.Fatalf("its containers were not made again with the new value: %+v", compose.created)
	}
}

func TestAChangedGlobalBringsARunningAppUpAgain(t *testing.T) {
	app, compose := appForGlobals(t), &fakeCompose{}

	if err := app.reapplyGlobals(context.Background(), compose, lists([2]string{"c1", "running"}, [2]string{"c2", "exited"})); err != nil {
		t.Fatal(err)
	}
	if compose.created == nil || !compose.startCalled {
		t.Fatal("the running app was not brought up again with the new value")
	}
}

// An app with no container has nothing to give the value to: what creates its containers
// reads the variables then. And compose's create starts a container that is neither running
// nor stopped: such an app is left as it is.
func TestAChangedGlobalLeavesAnAppThatIsNeitherRunningNorStopped(t *testing.T) {
	for what, containerLists := range map[string]map[string][]codegen.ContainerSummary{
		"no container":     lists(),
		"a dead container": lists([2]string{"c1", "exited"}, [2]string{"c2", "dead"}),
	} {
		app, compose := appForGlobals(t), &fakeCompose{}

		if err := app.reapplyGlobals(context.Background(), compose, containerLists); err != nil {
			t.Fatal(err)
		}
		if compose.created != nil || compose.startCalled {
			t.Errorf("%s: compose was asked to create or start", what)
		}
	}
}

// A backup that holds the app stopped starts the containers it stopped when it is done: they
// are not made again under it.
func TestAChangedGlobalWaitsForWhatHoldsTheApp(t *testing.T) {
	app := appForGlobals(t)
	end, err := Begin(app.Name, "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer end()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // nobody waits the whole patience in a test

	var busy ErrAppBusy
	if err := app.ReapplyGlobals(ctx, nil); !errors.As(err, &busy) {
		t.Fatalf("want the app left to what holds it, got %v", err)
	}
}
