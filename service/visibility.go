package service

import (
	stdjson "encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// VisibilityPath holds, per compose project, the accounts it is hidden from.
// A project with no entry is visible to everyone.
var VisibilityPath = "/var/lib/casaos/visibility.json"

// AppVisibility is the box's one list.
var AppVisibility = &Visibility{}

// Visibility reads the file at each call: it is small, and an edit made by another
// process or a restore is seen at once.
type Visibility struct {
	mu sync.RWMutex
}

// read returns the entries. A file that will not parse is an error: readers treat it as
// "nothing hidden" and log it, writers stop, so the owner's lists are not replaced by an
// empty one.
func (v *Visibility) read() (map[string][]int, error) {
	content, err := os.ReadFile(VisibilityPath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return map[string][]int{}, nil
	}
	if err != nil {
		return nil, err
	}

	entries := map[string][]int{}
	if err := stdjson.Unmarshal(content, &entries); err != nil {
		return nil, err
	}

	return entries, nil
}

func (v *Visibility) readOrNothing() map[string][]int {
	entries, err := v.read()
	if err != nil {
		logger.Error("the visibility file cannot be read, so nothing is hidden", zap.Error(err), zap.String("path", VisibilityPath))
		return map[string][]int{}
	}

	return entries
}

// Get returns the accounts app is hidden from.
func (v *Visibility) Get(app string) []int {
	v.mu.RLock()
	defer v.mu.RUnlock()

	return append([]int{}, v.readOrNothing()[app]...)
}

// HiddenFor reports whether app is hidden from the account.
func (v *Visibility) HiddenFor(app string, account int) bool {
	return HiddenIn(v.Snapshot(), app, account)
}

// Snapshot is the whole file, read once: a list that asks about many apps takes one and
// asks HiddenIn, rather than parsing the file for each.
func (v *Visibility) Snapshot() map[string][]int {
	v.mu.RLock()
	defer v.mu.RUnlock()

	return v.readOrNothing()
}

// HiddenIn reports whether app is hidden from the account in a snapshot.
func HiddenIn(snapshot map[string][]int, app string, account int) bool {
	return slices.Contains(snapshot[app], account)
}

// update reads, lets change edit, drops empty entries and writes.
func (v *Visibility) update(change func(map[string][]int)) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	entries, err := v.read()
	if err != nil {
		return err
	}
	change(entries)
	for app, ids := range entries {
		if len(ids) == 0 {
			delete(entries, app)
		}
	}

	encoded, err := stdjson.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	return writeGitFile(VisibilityPath, encoded) // temporary file, sync, rename, 0600
}

// ErrBadAccountID is an id no account has: SQLite counts from 1.
var ErrBadAccountID = fmt.Errorf("an account id is 1 or more")

// Hide replaces the accounts app is hidden from; none shows it to everyone again. Each id is
// kept once, in the order given, and an id below 1 refuses the whole list.
func (v *Visibility) Hide(app string, accounts []int) error {
	if slices.ContainsFunc(accounts, func(id int) bool { return id < 1 }) {
		return ErrBadAccountID
	}
	seen := map[int]bool{}
	unique := []int{}
	for _, id := range accounts {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}

	return v.update(func(entries map[string][]int) { entries[app] = unique })
}

// RemoveApp forgets app. An uninstall does not: the app's logs, stats and backups stay, so it
// stays hidden; the owner removes an entry left by an uninstalled app.
func (v *Visibility) RemoveApp(app string) error {
	return v.update(func(entries map[string][]int) { delete(entries, app) })
}

// RemoveAccount takes the account out of every entry, so that a later account given
// the same id does not inherit them.
func (v *Visibility) RemoveAccount(id int) error {
	return v.update(func(entries map[string][]int) {
		for app, ids := range entries {
			kept := []int{}
			for _, other := range ids {
				if other != id {
					kept = append(kept, other)
				}
			}
			entries[app] = kept
		}
	})
}
