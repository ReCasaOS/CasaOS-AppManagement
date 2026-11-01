package restic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// one line per node, the snapshot first, as `restic ls --json` prints them
const lsOutput = `{"time":"2026-10-02T10:00:00Z","paths":["/vol/data"],"id":"ccc","struct_type":"snapshot"}
{"name":"data","type":"dir","path":"/vol/data","size":0,"struct_type":"node"}
{"name":"notes.txt","type":"file","path":"/vol/data/notes.txt","size":12,"mtime":"2026-10-01T09:00:00Z","struct_type":"node"}
{"name":"docs","type":"dir","path":"/vol/data/docs","struct_type":"node"}
{"name":"deep.txt","type":"file","path":"/vol/data/docs/deep.txt","size":3,"struct_type":"node"}
{"name":"link","type":"symlink","path":"/vol/data/link","struct_type":"node"}
`

func TestListingAFolderGivesItsChildrenOnly(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil), "ls": reply(lsOutput, nil)}}
	e := engineWith(t, f)

	files, err := e.List(context.Background(), "nas", "blog/20261002/volume-data", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.last(), " "); got != "ls --json ccc /vol/data" {
		t.Fatalf("asked %q", got)
	}

	names := []string{}
	for _, file := range files {
		names = append(names, file.Name)
	}
	// not the folder itself, not what is below docs
	if got := strings.Join(names, " "); got != "notes.txt docs link" {
		t.Fatalf("got %q", got)
	}
	if files[0].Size != 12 || files[0].Directory || !files[1].Directory || files[2].Directory {
		t.Fatalf("%+v", files)
	}
}

func TestListingBelowAFolderAsksForThatFolder(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil), "ls": reply(`{"struct_type":"snapshot"}`, nil)}}
	e := engineWith(t, f)

	if _, err := e.List(context.Background(), "nas", "blog/20261002/volume-data", "docs/old"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.last(), " "); got != "ls --json ccc /vol/data/docs/old" {
		t.Fatalf("asked %q", got)
	}
}

func TestACopyThatIsOneFileListsAsThatFile(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){
		"snapshots": reply(`[{"id":"fff","time":"2026-10-02T10:00:00Z","paths":["/etc/app.env"],"tags":["casaos","key=blog/20261002/env"]}]`, nil),
		"ls":        reply(`{"struct_type":"snapshot"}`+"\n"+`{"name":"app.env","type":"file","path":"/etc/app.env","size":40,"struct_type":"node"}`, nil),
	}}
	e := engineWith(t, f)

	files, err := e.List(context.Background(), "nas", "blog/20261002/env", "")
	if err != nil || len(files) != 1 || files[0].Name != "app.env" || files[0].Directory {
		t.Fatalf("%+v %v", files, err)
	}
}

func TestListingWhatTheRepositoryDoesNotHoldSaysSo(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil)}}
	e := engineWith(t, f)

	if _, err := e.List(context.Background(), "nas", "blog/20261002/volume-gone", ""); !errors.Is(err, ErrNothingHeld) {
		t.Fatalf("err %v", err)
	}
}

func TestTakingBackAPathRestoresOnlyIt(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil)}}
	e := engineWith(t, f)

	if err := e.CopyBack(context.Background(), "nas", "blog/20261002/volume-data", "docs/deep.txt", "/host/data/docs/deep.txt"); err != nil {
		t.Fatal(err)
	}
	// the folder it sits in is the root to restore from; the host side is that folder too
	want := "restore ccc:/vol/data/docs --target /host/data/docs --include /deep.txt"
	if got := strings.Join(f.last(), " "); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestTakingBackNeverDeletesWhatIsThereNow(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil), "ls": reply(lsOutput, nil)}}
	e := engineWith(t, f)

	for _, sub := range []string{"docs", ""} {
		if err := e.CopyBack(context.Background(), "nas", "blog/20261002/volume-data", sub, "/host/data/"+sub); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(f.last(), " "); strings.Contains(got, "--delete") {
			t.Fatalf("got %q", got)
		}
	}
}

func TestTakingBackTheWholeFolderPutsItsContentsInTheFolder(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil), "ls": reply(lsOutput, nil)}}
	e := engineWith(t, f)

	if err := e.CopyBack(context.Background(), "nas", "blog/20261002/volume-data", "", "/host/data"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.last(), " "); got != "restore ccc:/vol/data --target /host/data" {
		t.Fatalf("got %q", got)
	}
}

func TestTakingBackAFileCopyWritesThatFile(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){
		"snapshots": reply(`[{"id":"fff","time":"2026-10-02T10:00:00Z","paths":["/etc/app.env"],"tags":["casaos","key=blog/20261002/env"]}]`, nil),
		"ls":        reply(`{"name":"app.env","type":"file","path":"/etc/app.env","size":4,"struct_type":"node"}`, nil),
		"dump":      reply("KEY=1", nil),
	}}
	e := engineWith(t, f)

	host := filepath.Join(t.TempDir(), "app.env")
	if err := e.CopyBack(context.Background(), "nas", "blog/20261002/env", "", host); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(host); err != nil || string(content) != "KEY=1" {
		t.Fatalf("%q %v", content, err)
	}
}

func TestANameIsNotReadAsAPattern(t *testing.T) {
	f := &fake{replies: map[string]func() ([]byte, error){"snapshots": reply(held, nil)}}
	e := engineWith(t, f)

	if err := e.CopyBack(context.Background(), "nas", "blog/20261002/volume-data", "we[ird]*?.txt", "/host/we[ird]*?.txt"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.last(), " "); !strings.HasSuffix(got, `--include /we\[ird]\*\?.txt`) {
		t.Fatalf("got %q", got)
	}
}
