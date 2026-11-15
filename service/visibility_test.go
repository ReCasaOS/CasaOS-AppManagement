package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func visibilityIn(t *testing.T) *Visibility {
	t.Helper()
	was := VisibilityPath
	VisibilityPath = filepath.Join(t.TempDir(), "visibility.json")
	t.Cleanup(func() { VisibilityPath = was })

	return &Visibility{}
}

func TestHideAndReadBack(t *testing.T) {
	v := visibilityIn(t)

	if v.HiddenFor("jellyfin", 2) || len(v.Get("jellyfin")) != 0 {
		t.Fatal("a new app is visible to everyone")
	}
	if err := v.Hide("jellyfin", []int{2, 3}); err != nil {
		t.Fatal(err)
	}
	if !v.HiddenFor("jellyfin", 2) || !v.HiddenFor("jellyfin", 3) || v.HiddenFor("jellyfin", 4) || v.HiddenFor("other", 2) {
		t.Fatal("hidden for 2 and 3 only, and only that app")
	}
	if got := v.Get("jellyfin"); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Fatalf("Get = %v", got)
	}
	// an empty list shows the app again and leaves no entry behind
	if err := v.Hide("jellyfin", nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(VisibilityPath)
	if strings.Contains(string(raw), "jellyfin") {
		t.Fatalf("empty list kept an entry: %s", raw)
	}
}

func TestWriteIsAtomicAnd0600(t *testing.T) {
	v := visibilityIn(t)
	if err := v.Hide("a", []int{2}); err != nil {
		t.Fatal(err)
	}
	if err := v.Hide("b", []int{3}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(VisibilityPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(VisibilityPath))
	if len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
}

func TestRemoveAccountClearsEveryEntry(t *testing.T) {
	v := visibilityIn(t)
	_ = v.Hide("a", []int{2, 3})
	_ = v.Hide("b", []int{3})
	_ = v.Hide("c", []int{4})

	if err := v.RemoveAccount(3); err != nil {
		t.Fatal(err)
	}
	if v.HiddenFor("a", 3) || v.HiddenFor("b", 3) || !v.HiddenFor("a", 2) || !v.HiddenFor("c", 4) {
		t.Fatal("account 3 left, the others stay")
	}
	if got := v.Get("b"); len(got) != 0 {
		t.Fatalf("b = %v", got)
	}
	// unknown id: nothing to do, no error
	if err := v.RemoveAccount(99); err != nil {
		t.Fatal(err)
	}
	if err := v.RemoveApp("a"); err != nil || v.HiddenFor("a", 2) {
		t.Fatalf("RemoveApp: %v", err)
	}
}

func TestCorruptFileMeansNothingHiddenButIsNotOverwrittenSilently(t *testing.T) {
	v := visibilityIn(t)
	const junk = "{ not json"
	if err := os.WriteFile(VisibilityPath, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}

	if v.HiddenFor("a", 2) || len(v.Get("a")) != 0 {
		t.Fatal("an unreadable file hides nothing")
	}
	if v.Hide("a", []int{2}) == nil || v.RemoveAccount(2) == nil || v.RemoveApp("a") == nil {
		t.Fatal("a write over an unreadable file must fail")
	}
	if raw, _ := os.ReadFile(VisibilityPath); string(raw) != junk {
		t.Fatalf("the file was overwritten: %s", raw)
	}
}

// An uninstall, an update or a reinstall keeps who an app is hidden from: its logs, stats
// and backups outlive it. Nothing in this package removes an entry; the owner does, through
// the visibility routes.
func TestUninstallKeepsVisibilityEntry(t *testing.T) {
	callers := map[string]string{}
	files, _ := filepath.Glob("*.go")
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RemoveApp" {
						callers[fn.Name.Name] = name
					}
				}
				return true
			})
		}
	}
	if len(callers) != 0 {
		t.Fatalf("an entry is removed only by the owner, callers: %v", callers)
	}
}

func TestHideDedupesAndRefusesIdsBelowOne(t *testing.T) {
	v := visibilityIn(t)

	if err := v.Hide("a", []int{3, 2, 3, 2}); err != nil {
		t.Fatal(err)
	}
	if got := v.Get("a"); !reflect.DeepEqual(got, []int{3, 2}) {
		t.Fatalf("ids are kept once, in the order given: %v", got)
	}
	for _, bad := range [][]int{{0}, {-1}, {2, 0}} {
		if err := v.Hide("a", bad); err == nil {
			t.Fatalf("%v must be refused", bad)
		}
	}
	if got := v.Get("a"); !reflect.DeepEqual(got, []int{3, 2}) {
		t.Fatalf("a refused list changed nothing: %v", got)
	}
}

func TestSnapshotIsOneReadForManyApps(t *testing.T) {
	v := visibilityIn(t)
	_ = v.Hide("a", []int{2})
	_ = v.Hide("b", []int{3})

	snapshot := v.Snapshot()
	// the file is gone: the snapshot still answers, so a list pays for one read
	_ = os.Remove(VisibilityPath)

	if !HiddenIn(snapshot, "a", 2) || HiddenIn(snapshot, "a", 3) || !HiddenIn(snapshot, "b", 3) || HiddenIn(snapshot, "c", 2) {
		t.Fatalf("snapshot = %v", snapshot)
	}
	if HiddenIn(nil, "a", 2) {
		t.Fatal("no snapshot hides nothing")
	}
}
