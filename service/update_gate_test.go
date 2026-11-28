package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/logstore"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
)

type gateCall struct{ Current, Candidate string }

// theGate sets a module in place, replaces compareImages with answer and returns what it was asked.
func theGate(t *testing.T, module bool, answer func(ctx context.Context, current, candidate string) (string, int, int, []codegen.BlockedFinding, error)) *[]gateCall {
	t.Helper()
	setRuntimePathForTest(t)
	if module {
		assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, "vuln-scan.url"), []byte("http://127.0.0.1:1"), 0o600))
	}
	calls := &[]gateCall{}
	old := compareImages
	t.Cleanup(func() { compareImages = old })
	compareImages = func(ctx context.Context, current, candidate string) (string, int, int, []codegen.BlockedFinding, error) {
		*calls = append(*calls, gateCall{current, candidate})
		if answer == nil {
			return "not_worse", 0, 0, nil, nil
		}
		return answer(ctx, current, candidate)
	}
	return calls
}

func worse(_ context.Context, _, _ string) (string, int, int, []codegen.BlockedFinding, error) {
	return "worse", 1, 3, []codegen.BlockedFinding{{Id: "CVE-1"}}, nil
}

func gateApp(images map[string]string) *ComposeApp {
	services := types.Services{}
	for name, ref := range images {
		services[name] = types.ServiceConfig{Name: name, Image: ref}
	}
	return &ComposeApp{Name: "web", Services: services}
}

func gateDaemon(tags map[string]string, containers ...container.Summary) *fakeTagger {
	return &fakeTagger{tags: tags, containers: containers}
}

func TestGateSkipsWithoutModule(t *testing.T) {
	calls := theGate(t, false, worse)
	d := gateDaemon(map[string]string{"app:2": "sha256:new"}, aContainer("web", "app", "sha256:old"))

	assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": "app:2"})))
	assert.Equal(t, len(*calls), 0)
}

func TestGateSkipsUnchangedImage(t *testing.T) {
	calls := theGate(t, true, worse)
	d := gateDaemon(map[string]string{"app:1": "sha256:same"}, aContainer("web", "app", "sha256:same"))
	app := gateApp(map[string]string{"app": "app:1"})

	assert.NilError(t, checkCandidates(context.Background(), d, app, app))
	assert.Equal(t, len(*calls), 0)
}

func TestGateBlocksOnWorse(t *testing.T) {
	theGate(t, true, worse)
	d := gateDaemon(map[string]string{"app:1": "sha256:new"}, aContainer("web", "app", "sha256:old"))
	d.digests = map[string][]string{"app:1": {"app@sha256:aa"}}

	err := checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": "app:1"}))

	var blocked *UpdateBlockedError
	assert.Assert(t, errors.As(err, &blocked), "%v", err)
	assert.Equal(t, len(blocked.Images), 1)
	assert.DeepEqual(t, blocked.Images[0], BlockedImage{
		Service: "app", Image: "app:1", RepoDigests: []string{"app@sha256:aa"}, ID: "sha256:new",
		Current: 1, Candidate: 3, Added: []codegen.BlockedFinding{{Id: "CVE-1"}},
	})
	assert.Equal(t, err.Error(), "the new version has 2 more critical vulnerabilities than the one installed")

	one := &UpdateBlockedError{Images: []BlockedImage{{Current: 1, Candidate: 2}}}
	assert.Equal(t, one.Error(), "the new version has 1 more critical vulnerability than the one installed")
}

func TestGatePassesOnUnknownNotWorseAndErrors(t *testing.T) {
	for name, answer := range map[string]func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error){
		"unknown": func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
			return "unknown", 0, 0, nil, nil
		},
		"not_worse": func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
			return "not_worse", 1, 1, nil, nil
		},
		"network": func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
			return "", 0, 0, nil, errors.New("connection refused")
		},
		"404": func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
			return "", 0, 0, nil, errors.New("status 404")
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := theGate(t, true, answer)
			d := gateDaemon(map[string]string{"app:2": "sha256:new"}, aContainer("web", "app", "sha256:old"))

			assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": "app:2"})))
			assert.Equal(t, len(*calls), 1)
		})
	}
}

// The tag stays `app:1.0` in the running app; its containers run what it pointed at then.
func TestGateUsesContainerImageWhenTagChanges(t *testing.T) {
	calls := theGate(t, true, nil)
	d := gateDaemon(map[string]string{"app:1.0": "sha256:other", "app:1.1": "sha256:new"}, aContainer("web", "app", "sha256:running"))

	assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1.0"}), gateApp(map[string]string{"app": "app:1.1"})))
	assert.DeepEqual(t, *calls, []gateCall{{"sha256:running", "sha256:new"}})
}

func TestGateComparesDigestPinnedService(t *testing.T) {
	calls := theGate(t, true, nil)
	pinned := "app@sha256:" + "ab"
	d := gateDaemon(map[string]string{pinned: "sha256:new"}, aContainer("web", "app", "sha256:old"))

	assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": pinned})))
	assert.DeepEqual(t, *calls, []gateCall{{"sha256:old", "sha256:new"}})
}

func TestGateSkipsServiceWithoutContainer(t *testing.T) {
	calls := theGate(t, true, worse)
	d := gateDaemon(map[string]string{"app:2": "sha256:new"})

	assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": "app:2"})))
	assert.Equal(t, len(*calls), 0)
}

func TestGateGivesUpAfterTimeout(t *testing.T) {
	theGate(t, true, func(ctx context.Context, _, _ string) (string, int, int, []codegen.BlockedFinding, error) {
		<-ctx.Done()
		return "", 0, 0, nil, ctx.Err()
	})
	old := gateTimeout
	t.Cleanup(func() { gateTimeout = old })
	gateTimeout = 20 * time.Millisecond
	d := gateDaemon(map[string]string{"app:2": "sha256:new"}, aContainer("web", "app", "sha256:old"))

	assert.NilError(t, checkCandidates(context.Background(), d, gateApp(map[string]string{"app": "app:1"}), gateApp(map[string]string{"app": "app:2"})))
}

func TestGateWritesUncheckedToTheAppJournal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := logstore.Open(ctx, t.TempDir())
	assert.NilError(t, err)
	SetLogs(store)
	t.Cleanup(func() { SetLogs(nil); _ = store.Close() })
	unchecked := func() []logstore.Event {
		events, err := store.Events(logstore.EventQuery{Apps: []string{"web"}, Kinds: []string{"update-unchecked"}})
		assert.NilError(t, err)
		return events
	}
	theClient := compareImages
	// moduleURL, when set, is where the module answers instead of nowhere
	run := func(answer func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error), module bool, moduleURL string) {
		theGate(t, module, answer)
		if moduleURL != "" {
			assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, external.InternalSecretFilename), []byte("s3cret-value"), 0o600))
			assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, "vuln-scan.url"), []byte(moduleURL), 0o600))
		}
		d := gateDaemon(map[string]string{"app:2": "sha256:new", "db:2": "sha256:newdb"},
			aContainer("web", "app", "sha256:old"), aContainer("web", "db", "sha256:olddb"))
		assert.NilError(t, checkCandidates(context.Background(), d,
			gateApp(map[string]string{"app": "app:1", "db": "db:1"}), gateApp(map[string]string{"app": "app:2", "db": "db:2"})))
	}

	run(nil, false, "")
	run(nil, true, "")
	assert.Equal(t, len(unchecked()), 0)

	// a module that cannot be reached, or answers anything but 200 (an older one says 400),
	// lets the update through without a line
	run(func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
		return "", 0, 0, nil, errors.New("connection refused")
	}, true, "")
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer older.Close()
	run(theClient, true, older.URL)
	assert.Equal(t, len(unchecked()), 0)

	// two services, both unknown: one line, with the first reason
	run(func(context.Context, string, string) (string, int, int, []codegen.BlockedFinding, error) {
		return "unknown", 0, 0, nil, errors.New("boom")
	}, true, "")
	events := unchecked()
	assert.Equal(t, len(events), 1)
	assert.Equal(t, events[0].Detail, "boom")

	old := gateTimeout
	t.Cleanup(func() { gateTimeout = old })
	gateTimeout = 20 * time.Millisecond
	run(func(ctx context.Context, _, _ string) (string, int, int, []codegen.BlockedFinding, error) {
		<-ctx.Done()
		return "", 0, 0, nil, ctx.Err()
	}, true, "")
	assert.Equal(t, len(unchecked()), 2)
}

// The real client: the route, the body, the credential, and an error for anything but 200.
func TestCompareImagesAsksTheModule(t *testing.T) {
	var got map[string]string
	status := http.StatusOK
	module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/v2/vuln_scan/compare")
		assert.Equal(t, r.Header.Get("Authorization"), "Internal s3cret-value")
		assert.NilError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"data":{"verdict":"worse","current_critical":1,"candidate_critical":3,"added":[{"id":"CVE-1","package":"p","fixed":"2","title":"t","url":"u"}],"reason":""}}`))
		}
	}))
	defer module.Close()
	setRuntimePathForTest(t)
	assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, external.InternalSecretFilename), []byte("s3cret-value"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, "vuln-scan.url"), []byte(module.URL+"\n"), 0o600))

	verdict, cur, cand, added, err := compareImages(context.Background(), "sha256:a", "sha256:b")
	assert.NilError(t, err)
	assert.Equal(t, verdict, "worse")
	assert.Equal(t, cur, 1)
	assert.Equal(t, cand, 3)
	assert.DeepEqual(t, added, []codegen.BlockedFinding{{Id: "CVE-1", Package: "p", Fixed: "2", Title: "t", Url: "u"}})
	assert.DeepEqual(t, got, map[string]string{"current": "sha256:a", "candidate": "sha256:b"})

	status = http.StatusNotFound
	_, _, _, _, err = compareImages(context.Background(), "sha256:a", "sha256:b")
	assert.Assert(t, err != nil)
}

func TestCompareImagesNeverLeavesLoopback(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the scan module address was contacted")
	}))
	defer elsewhere.Close()
	setRuntimePathForTest(t)
	for _, base := range []string{"http://example.com", "https://" + elsewhere.Listener.Addr().String(), "http://192.0.2.1:80"} {
		assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, "vuln-scan.url"), []byte(base), 0o600))
		_, _, _, _, err := compareImages(context.Background(), "a", "b")
		assert.ErrorContains(t, err, "the scan module address is not on loopback", base)
	}
}

func TestCompareImagesDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the redirect was followed")
	}))
	defer target.Close()
	module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer module.Close()
	setRuntimePathForTest(t)
	assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, "vuln-scan.url"), []byte(module.URL), 0o600))

	_, _, _, _, err := compareImages(context.Background(), "a", "b")
	assert.Assert(t, err != nil)
}
