package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

// The log viewer shows when each line was written, and only the daemon knows that, so
// Logs must ask for it. Nothing else in this package would notice if it stopped.
func TestLogsAskTheDaemonForTimestamps(t *testing.T) {
	app := &ComposeApp{Services: types.Services{
		"web": types.ServiceConfig{Name: "web"},
		"db":  types.ServiceConfig{Name: "db"},
	}}

	options := app.logOptions(100)

	if !options.Timestamps {
		t.Error("Logs no longer asks the daemon for per-line timestamps")
	}
	if options.Follow {
		t.Error("Logs asked to follow the stream, so it would never return")
	}
	if options.Tail != "100" {
		t.Errorf("tail is %q, want the number of lines asked for", options.Tail)
	}
	if strings.Join(options.Services, ",") != "db,web" {
		t.Errorf("services are %v, want every service of the app", options.Services)
	}

	// the viewer asks for everything as a negative count, which docker spells "all"
	if tail := app.logOptions(-1).Tail; tail != "all" {
		t.Errorf("tail for a negative count is %q, want %q", tail, "all")
	}
}

// The per-container viewer asks for ONE service. If that name stopped reaching the
// daemon, the panel would quietly go back to showing the whole stack interleaved --
// which is exactly what it looks like when it is right.
func TestLogsAskTheDaemonForOneNamedService(t *testing.T) {
	app := &ComposeApp{Services: types.Services{
		"web": types.ServiceConfig{Name: "web"},
		"db":  types.ServiceConfig{Name: "db"},
	}}

	options := app.logOptions(100, "db")

	if strings.Join(options.Services, ",") != "db" {
		t.Errorf("services are %v, want only the one asked for", options.Services)
	}
}

// And the consumer must leave that timestamp alone. Its own timestamp flag stamps
// time.Now() at READ time, which would put this instant in front of the daemon's real
// one on every line of a tail.
func TestLogConsumerKeepsDaemonTimestamp(t *testing.T) {
	var buf bytes.Buffer

	consumer := newLogConsumer(context.Background(), &buf)

	const line = "2026-09-09T10:11:12.130000000Z listening on :8080"
	consumer.Log("whoami", line)

	got := buf.String()
	if !strings.HasSuffix(got, line+"\n") {
		t.Fatalf("expected the daemon line verbatim, got %q", got)
	}

	// whatever precedes the daemon line is the prefix, and it is only the service name
	prefix := strings.TrimRight(strings.TrimSuffix(got, line+"\n"), "| ")
	if prefix != "whoami" {
		t.Fatalf("expected only the service name before the daemon line, got %q", prefix)
	}
}
