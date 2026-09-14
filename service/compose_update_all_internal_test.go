package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

func composeFor(t *testing.T, yaml string) *ComposeApp {
	t.Helper()
	app, err := NewComposeAppFromYAML([]byte(yaml), true, false)
	if err != nil {
		t.Fatal(err)
	}

	return app
}

func TestThePlanSaysWhatMovesAndWhy(t *testing.T) {
	logger.LogInitConsoleOnly()

	installed := map[string]*ComposeApp{
		"alpha": composeFor(t, "name: alpha\nservices:\n  web:\n    image: nginx:1.25\n  db:\n    image: postgres:16\nx-casaos:\n  main: web\n  store_app_id: alpha\n  title:\n    en_us: Alpha\n  icon: https://icons/alpha.png\n"),
		"beta":  composeFor(t, "name: beta\nservices:\n  app:\n    image: ghcr.io/x/beta:latest\n"),
		"gamma": composeFor(t, "name: gamma\nservices:\n  app:\n    image: gamma:2\nx-casaos:\n  main: app\n  store_app_id: gamma\n"),
	}
	catalogue := map[string]*ComposeApp{
		"alpha": composeFor(t, "name: alpha\nservices:\n  web:\n    image: nginx:1.27\n  db:\n    image: postgres:16\nx-casaos:\n  main: web\n  store_app_id: alpha\n"),
	}
	available := func(app *ComposeApp) bool { return app.Name != "gamma" }
	storeCompose := func(id string) (*ComposeApp, error) {
		if app, ok := catalogue[id]; ok {
			return app, nil
		}

		return nil, errors.New("not in the catalogue")
	}

	plan := PlanUpdateAll(installed, available, storeCompose)
	if len(plan) != 2 {
		t.Fatalf("two apps have an update on offer, got %+v", plan)
	}

	alpha := plan[0]
	if alpha.Id != "alpha" || alpha.Title != "Alpha" || alpha.Icon != "https://icons/alpha.png" || alpha.Kind != codegen.Catalogue {
		t.Fatalf("alpha: %+v", alpha)
	}
	if len(alpha.Services) != 1 || alpha.Services[0].Name != "web" || alpha.Services[0].From != "nginx:1.25" || alpha.Services[0].To != "nginx:1.27" {
		t.Fatalf("only the image the catalogue moves is listed: %+v", alpha.Services)
	}

	beta := plan[1]
	if beta.Id != "beta" || beta.Title != "beta" || beta.Kind != codegen.Image {
		t.Fatalf("an app with no catalogue entry is a re-pull, named by its name: %+v", beta)
	}
	if len(beta.Services) != 1 || beta.Services[0].From != "ghcr.io/x/beta:latest" || beta.Services[0].To != "ghcr.io/x/beta:latest" {
		t.Fatalf("every service, as it is: %+v", beta.Services)
	}
}

func TestTheRunGoesOneAppAtATimeAndOnlyOneRunAtATime(t *testing.T) {
	updateAll.run = nil
	t.Cleanup(func() { updateAll.run = nil })

	order := []string{}
	release := make(chan struct{})
	update := func(_ context.Context, id string) (codegen.ComposeAppUpdateRunAppState, string) {
		order = append(order, id)
		<-release
		if id == "two" {
			return codegen.ComposeAppUpdateRunAppStateFailed, "the registry said no"
		}

		return codegen.ComposeAppUpdateRunAppStateUpdated, ""
	}

	run, err := StartUpdateAll([]string{"one", "two", "three"}, update)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Apps) != 3 || run.Apps[0].State != codegen.ComposeAppUpdateRunAppStateQueued && run.Apps[0].State != codegen.ComposeAppUpdateRunAppStateUpdating {
		t.Fatalf("the run starts with the list, queued: %+v", run.Apps)
	}

	if _, err := StartUpdateAll([]string{"four"}, update); !errors.Is(err, ErrUpdateAllRunning) {
		t.Fatalf("a second run while one is going is refused, got %v", err)
	}

	for range 3 {
		release <- struct{}{}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		current := CurrentUpdateAllRun()
		if current.FinishedAt != nil {
			run = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(order) != 3 || order[0] != "one" || order[1] != "two" || order[2] != "three" {
		t.Fatalf("in the order given, one after another: %v", order)
	}
	if run.Apps[0].State != codegen.ComposeAppUpdateRunAppStateUpdated || run.Apps[2].State != codegen.ComposeAppUpdateRunAppStateUpdated {
		t.Fatalf("what became of each: %+v", run.Apps)
	}
	if run.Apps[1].State != codegen.ComposeAppUpdateRunAppStateFailed || run.Apps[1].Message == nil || *run.Apps[1].Message != "the registry said no" {
		t.Fatalf("a failure keeps its reason and the run goes on: %+v", run.Apps[1])
	}

	if _, err := StartUpdateAll([]string{"four"}, update); err != nil {
		t.Fatalf("a finished run makes way for the next: %v", err)
	}
	release <- struct{}{}
}
