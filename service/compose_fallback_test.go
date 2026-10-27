package service

import (
	"strings"
	"testing"
)

// A text pasted at install time gives a default, or a message, for a variable
// nothing sets: the default is used, the message is the error. Before, every
// unknown variable became its own name, so ${FILES:-4} was "$FILES" and a
// number in it failed to load.
func TestADefaultOrAMessageAppliesToAVariableNothingSets(t *testing.T) {
	app, err := NewComposeAppFromYAML([]byte(`name: stack
services:
  queue:
    image: alpine:3
    deploy:
      replicas: ${FILES:-4}
    environment:
      URL: ${ENGINE_URL:-http://engine:8770}
      KEPT: $SET_LATER
`), false, true)
	if err != nil {
		t.Fatal(err)
	}
	queue := app.Services["queue"]
	if queue.Deploy == nil || queue.Deploy.Replicas == nil || *queue.Deploy.Replicas != 4 {
		t.Fatalf("replicas %+v", queue.Deploy)
	}
	if got := *queue.Environment["URL"]; got != "http://engine:8770" {
		t.Errorf("URL = %q", got)
	}
	// a variable without a default still waits for the app's settings
	if got := *queue.Environment["KEPT"]; got != "$SET_LATER" {
		t.Errorf("KEPT = %q", got)
	}

	_, err = NewComposeAppFromYAML([]byte(`name: stack
services:
  db:
    image: mariadb:11
    environment:
      MARIADB_PASSWORD: ${DB_PASSWORD:?DB_PASSWORD is missing}
`), false, true)
	if err == nil || !strings.Contains(err.Error(), "DB_PASSWORD is missing") {
		t.Fatalf("a required variable: %v", err)
	}

	t.Setenv("DB_PASSWORD", "secret")
	app, err = NewComposeAppFromYAML([]byte(`name: stack
services:
  db:
    image: mariadb:11
    environment:
      MARIADB_PASSWORD: ${DB_PASSWORD:?DB_PASSWORD is missing}
`), false, true)
	if err != nil || *app.Services["db"].Environment["MARIADB_PASSWORD"] != "secret" {
		t.Fatalf("a set variable: %v", err)
	}
}

func TestVariablesWithFallback(t *testing.T) {
	got := variablesWithFallback([]byte("a: ${A:-1} ${B-2} ${C:?x} ${D?y} ${E} $F ${G:+z}"))
	for _, name := range []string{"A", "B", "C", "D"} {
		if !got[name] {
			t.Errorf("%s is missing", name)
		}
	}
	for _, name := range []string{"E", "F", "G"} {
		if got[name] {
			t.Errorf("%s has no default", name)
		}
	}
}
