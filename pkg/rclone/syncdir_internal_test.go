package rclone

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/backupprogress"
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

// A count is a recursive listing: a large cloud tree takes longer than the client waits for an
// answer, so it runs as a job and its figure is read from the job's output.
func TestTheFileCountIsAJob(t *testing.T) {
	fastPolling(t)
	client, seen := serverFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/job/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 4, "finished": true, "success": true, "output": map[string]any{"count": 5, "bytes": 99}})

			return
		}
		_, _ = w.Write([]byte(`{"jobid":4}`))
	})

	count, err := client.CountFiles(t.Context(), "/a")
	if err != nil || count != 5 {
		t.Fatalf("count %d, %v", count, err)
	}
	first := (*seen)[0]
	if first.URL.Path != "/operations/size" || first.PostForm.Get("_async") != "true" || first.PostForm.Get("fs") != "/a" {
		t.Fatalf("not a job: %s %v", first.URL.Path, first.PostForm)
	}
}

// A sync job's figures are read once more after it ended: a short job ends between two polls.
// A count reports nothing, it is not what the pass moved.
func TestASyncJobReportsItsLastFigures(t *testing.T) {
	fastPolling(t)
	client, _ := serverFor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/job/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "finished": true, "success": true, "output": map[string]any{"count": 2}})
		case "/core/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"bytes": 42, "transfers": 3})
		default:
			_, _ = w.Write([]byte(`{"jobid":1}`))
		}
	})

	var got []backupprogress.Stats
	ctx := backupprogress.With(t.Context(), func(stats backupprogress.Stats) { got = append(got, stats) })
	if err := client.SyncDirectory(ctx, "/a", "/b", SyncOptions{MaxDelete: -1}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Files != 3 || got[0].Bytes != 42 {
		t.Fatalf("the last figures of the job: %+v", got)
	}
	if _, err := client.CountFiles(ctx, "/a"); err != nil || len(got) != 1 {
		t.Fatalf("a count reported %+v, %v", got, err)
	}
}

// configs answers config/get from a map of remotes, by their full name.
func configs(remotes map[string]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(remotes[r.PostForm.Get("name")])
	}
}

// Where a destination really writes: the backend, the account when its options can tell (and
// never a secret), and the folder of the account it is rooted at.
func TestADestinationsPlaceIsItsBackendAccountAndRoot(t *testing.T) {
	for what, c := range map[string]struct {
		remotes                map[string]map[string]any
		backend, account, root string
	}{
		"no root": {map[string]map[string]any{
			"casaos-backup-d": {"type": "sftp", "host": "nas.lan", "user": "gary", "port": "2222", "pass": "secret"},
		}, "sftp", "sftp|nas.lan|gary|2222", ""},
		"alias with a root": {map[string]map[string]any{
			"casaos-backup-d":     {"type": "alias", "remote": "casaos-backup-d-raw:/Backups/box1/"},
			"casaos-backup-d-raw": {"type": "drive", "token": `{"access_token":"x"}`},
		}, "drive", "", "Backups/box1"},
		"crypt with a root": {map[string]map[string]any{
			"casaos-backup-d":     {"type": "crypt", "remote": "casaos-backup-d-raw:vault", "password": "obscured"},
			"casaos-backup-d-raw": {"type": "webdav", "url": "https://dav.example", "user": "u", "pass": "p"},
		}, "webdav", "webdav|https://dav.example|u", "vault"},
		"s3": {map[string]map[string]any{
			"casaos-backup-d": {"type": "s3", "provider": "Other", "endpoint": "s3.example", "access_key_id": "AKIA1", "secret_access_key": "shh", "region": "eu"},
		}, "s3", "s3|Other|s3.example|AKIA1", ""},
		"s3 without a key": {map[string]map[string]any{
			"casaos-backup-d": {"type": "s3", "provider": "AWS", "env_auth": "true"},
		}, "s3", "", ""},
		"token backend": {map[string]map[string]any{
			"casaos-backup-d": {"type": "dropbox", "token": `{"access_token":"x"}`},
		}, "dropbox", "", ""},
	} {
		client, _ := serverFor(t, configs(c.remotes))
		backend, account, root, err := client.DestinationPlace("d")
		if err != nil || backend != c.backend || account != c.account || root != c.root {
			t.Fatalf("%s: got %q %q %q, %v", what, backend, account, root, err)
		}
		for _, secret := range []string{"secret", "shh", "obscured", "access_token", "\"p\""} {
			if strings.Contains(account, secret) {
				t.Fatalf("%s: a secret in the account: %q", what, account)
			}
		}
	}
}
