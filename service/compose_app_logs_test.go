package service_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/docker/compose/v2/cmd/formatter"
)

// ComposeApp.Logs asks the daemon for per-line timestamps, so the timestamp arrives as
// part of the message text. This pins the consumer's promise that it only prepends its
// service prefix and leaves the message alone, which is what the log viewer relies on.
func TestLogConsumerKeepsDaemonTimestamp(t *testing.T) {
	var buf bytes.Buffer

	consumer := formatter.NewLogConsumer(context.Background(), &buf, &buf, false, true, false)

	const line = "2026-09-09T10:11:12.130000000Z listening on :8080"
	consumer.Log("whoami", line)

	got := buf.String()
	if !strings.HasPrefix(got, "whoami") {
		t.Fatalf("expected the service name as prefix, got %q", got)
	}
	if !strings.HasSuffix(got, line+"\n") {
		t.Fatalf("expected the daemon line verbatim after the prefix, got %q", got)
	}
}
