package rclone

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// job answers a started job as finished, with the given error text ("" = success).
func job(failure string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/job/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "finished": true, "success": failure == "", "error": failure})

			return
		}
		_, _ = w.Write([]byte(`{"jobid":1}`))
	}
}

func TestOneWayWithoutMirrorNeverCallsSync(t *testing.T) {
	fastPolling(t)
	client, seen := serverFor(t, job(""))

	if err := client.SyncDirectory(t.Context(), "/a", "/b", SyncOptions{MaxDelete: 3}); err != nil {
		t.Fatal(err)
	}

	first := (*seen)[0]
	if first.URL.Path != "/sync/copy" {
		t.Fatalf("a copy must never become a sync: %s", first.URL.Path)
	}
	if got := first.PostForm.Get("_config"); got != "" {
		t.Fatalf("a copy has no cap and no archive to carry: %q", got)
	}
}

func TestMirrorPassesItsCapAndItsArchive(t *testing.T) {
	fastPolling(t)
	client, seen := serverFor(t, job(""))

	err := client.SyncDirectory(t.Context(), "/a", "/b", SyncOptions{Mirror: true, BackupDir: "/t/.b-archive/s", MaxDelete: 7})
	if err != nil {
		t.Fatal(err)
	}

	first := (*seen)[0]
	if first.URL.Path != "/sync/sync" || first.PostForm.Get("srcFs") != "/a" || first.PostForm.Get("dstFs") != "/b" {
		t.Fatalf("wrong call: %s %v", first.URL.Path, first.PostForm)
	}
	if got := first.PostForm.Get("_config"); got != `{"BackupDir":"/t/.b-archive/s","MaxDelete":7}` {
		t.Fatalf("_config: %s", got)
	}
}

func TestBisyncAlwaysCarriesItsSafetyOptions(t *testing.T) {
	fastPolling(t)
	client, seen := serverFor(t, job(""))

	if err := client.BisyncDirectory(t.Context(), "/p1", "/p2", BisyncOptions{Workdir: "/w"}); err != nil {
		t.Fatal(err)
	}

	first := (*seen)[0]
	if first.URL.Path != "/sync/bisync" {
		t.Fatalf("endpoint: %s", first.URL.Path)
	}
	want := map[string]string{
		"path1": "/p1", "path2": "/p2", "workdir": "/w",
		"resilient": "true", "recover": "true", "maxLock": "2m", "maxDelete": "50",
		"conflictResolve": "none", "createEmptySrcDirs": "true",
		"_config": `{"SuffixKeepExtension":true}`,
	}
	for key, value := range want {
		if got := first.PostForm.Get(key); got != value {
			t.Errorf("%s: want %q, got %q", key, value, got)
		}
	}
	for _, key := range []string{"resync", "force", "backupDir1", "backupDir2"} {
		if _, set := first.PostForm[key]; set {
			t.Errorf("%s must be absent when not asked for", key)
		}
	}
}

func TestBisyncErrorsAreTold(t *testing.T) {
	fastPolling(t)

	for failure, want := range map[string]error{
		"too many deletes":                               ErrTooManyDeletes,
		"\x1b[31mprior lock file found: /w/x.lck\x1b[0m": ErrSyncLocked,
		"bisync aborted":                                 ErrBisyncAborted,
	} {
		client, _ := serverFor(t, job(failure))

		if err := client.BisyncDirectory(t.Context(), "/p1", "/p2", BisyncOptions{}); !errors.Is(err, want) {
			t.Errorf("%q: want %v, got %v", failure, want, err)
		}
	}
}

func TestACappedMirrorSaysTooManyDeletes(t *testing.T) {
	fastPolling(t)
	client, _ := serverFor(t, job("failed to delete 5 files"))

	err := client.SyncDirectory(t.Context(), "/a", "/b", SyncOptions{Mirror: true, MaxDelete: 3})
	if !errors.Is(err, ErrTooManyDeletes) {
		t.Fatalf("got %v", err)
	}

	// without a cap the same words are an ordinary failure (a refused right, say)
	client, _ = serverFor(t, job("failed to delete 5 files"))
	err = client.SyncDirectory(t.Context(), "/a", "/b", SyncOptions{Mirror: true, MaxDelete: -1})
	if err == nil || errors.Is(err, ErrTooManyDeletes) {
		t.Fatalf("an uncapped mirror cannot have hit its cap: %v", err)
	}
}

func TestAnEncryptedDestinationIsTold(t *testing.T) {
	client, seen := serverFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.PostForm.Get("name") == "casaos-backup-x" {
			_, _ = w.Write([]byte(`{"type":"crypt"}`))

			return
		}
		_, _ = w.Write([]byte(`{"type":"s3"}`))
	})

	backend, encrypted, err := client.DestinationKind("x")
	if err != nil || backend != "s3" || !encrypted {
		t.Fatalf("got %q %v %v", backend, encrypted, err)
	}
	if (*seen)[1].PostForm.Get("name") != "casaos-backup-x-raw" {
		t.Fatalf("the backend sits under the raw name: %v", (*seen)[1].PostForm)
	}
}
