package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
)

// upgradeFlag stands in for the busy file of an operating system upgrade, and for the
// log line that says backups are waiting, so a test can choose what stat answers and
// count what was said about it.
type upgradeFlag struct {
	present bool  // the file is there
	err     error // stat fails with this instead, whatever present says
	info    os.FileInfo

	asked []string
	said  [][]zap.Field
}

func (u *upgradeFlag) stat(name string) (os.FileInfo, error) {
	u.asked = append(u.asked, name)

	switch {
	case u.err != nil:
		return nil, &fs.PathError{Op: "stat", Path: name, Err: u.err}
	case u.present:
		return u.info, nil
	}

	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (u *upgradeFlag) log(_ string, fields ...zap.Field) {
	u.said = append(u.said, fields)
}

func (u *upgradeFlag) field(line int, key string) (zap.Field, bool) {
	for _, field := range u.said[line] {
		if field.Key == key {
			return field, true
		}
	}

	return zap.Field{}, false
}

// upgradeFlagIn puts the fake where the scheduler looks, with no upgrade open. The real
// busy file is never read by a test: a box that is being upgraded must not change what
// the suite says.
func upgradeFlagIn(t *testing.T) *upgradeFlag {
	t.Helper()

	// something for stat to answer "it is there" with
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	flag := &upgradeFlag{info: info}
	wasStat, wasLog := osUpgradeBusyStat, logBackupsHeld
	osUpgradeBusyStat, logBackupsHeld = flag.stat, flag.log
	t.Cleanup(func() { osUpgradeBusyStat, logBackupsHeld = wasStat, wasLog })

	return flag
}

// The core is the only one that writes the file, and only its absence says there is no
// upgrade. Anything else the filesystem can answer is an upgrade nobody can rule out.
func TestOnlyAMissingBusyFileMeansNoUpgrade(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		err     error
		open    bool
	}{
		{name: "missing", open: false},
		{name: "there", present: true, open: true},
		{name: "not allowed", err: syscall.EACCES, open: true},
		{name: "not allowed, in the library's words", err: fs.ErrPermission, open: true},
		{name: "disk error", err: syscall.EIO, open: true},
		{name: "the directory is a file", err: syscall.ENOTDIR, open: true},
		{name: "anything else", err: errors.New("stale handle"), open: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flag := upgradeFlagIn(t)
			flag.present, flag.err = c.present, c.err

			open, err := osUpgradeOpen()
			if open != c.open {
				t.Fatalf("open = %v, want %v", open, c.open)
			}
			if c.err != nil && !errors.Is(err, c.err) {
				t.Fatalf("the reason is handed back: %v", err)
			}
			if c.err == nil && err != nil {
				t.Fatalf("no failure to report: %v", err)
			}

			// the file the core keeps: this is a promise between two services
			if len(flag.asked) != 1 || flag.asked[0] != "/var/lib/casaos/os-upgrade/busy" {
				t.Fatalf("asked about %v", flag.asked)
			}
		})
	}
}

// What the standard library says about a path that is not there, on this system's own
// terms, rather than what a fake has been told to say.
func TestTheRealFilesystemIsReadTheSameWay(t *testing.T) {
	upgradeFlagIn(t) // restores the seam; only stat is pointed somewhere real below

	where := filepath.Join(t.TempDir(), "busy")
	osUpgradeBusyStat = func(string) (os.FileInfo, error) { return os.Stat(where) }

	if open, err := osUpgradeOpen(); open || err != nil {
		t.Fatalf("nothing there: %v %v", open, err)
	}

	if err := writeRaw(where, "{}"); err != nil {
		t.Fatal(err)
	}
	if open, err := osUpgradeOpen(); !open || err != nil {
		t.Fatalf("the file is there: %v %v", open, err)
	}

	// A directory that is a file is not "nothing there". Windows words that answer
	// differently, and it is not where this runs.
	if runtime.GOOS != "windows" {
		osUpgradeBusyStat = func(string) (os.FileInfo, error) { return os.Stat(filepath.Join(where, "busy")) }

		if open, err := osUpgradeOpen(); !open || err == nil {
			t.Fatalf("a path through a file is not 'nothing there': %v %v", open, err)
		}
	}
}

func TestBackupsWaitWhileAnUpgradeIsOpen(t *testing.T) {
	schedulerInTempDir(t)
	flag := upgradeFlagIn(t)
	flag.present = true

	if err := SaveBackupSchedules([]BackupSchedule{daily(time.Time{})}); err != nil {
		t.Fatal(err)
	}

	// an old file, so that a rewrite would show
	longAgo := time.Now().Add(-time.Hour)
	if err := os.Chtimes(BackupSchedulePath, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.ran) != 0 {
		t.Fatalf("nothing runs through an upgrade: %v", runner.ran)
	}
	if info, err := os.Stat(BackupSchedulePath); err != nil || info.ModTime().After(longAgo.Add(time.Minute)) {
		t.Fatalf("a tick that ran nothing leaves the schedules as they were: %v %v", info, err)
	}
	if records, _ := BackupRuns(); len(records) != 0 {
		t.Fatalf("and no run is recorded: %+v", records)
	}
	if len(flag.said) != 1 {
		t.Fatalf("the tick says it held them back, once: %d", len(flag.said))
	}

	// The slot is not spent: a backup skipped for an upgrade is a backup owed, run once
	// when the upgrade is closed.
	schedules, err := BackupSchedules()
	if err != nil || len(schedules) != 1 || !schedules[0].LastRun.IsZero() {
		t.Fatalf("the slot is still unrun: %+v %v", schedules, err)
	}

	flag.present = false
	RunDueBackups(context.Background(), at(10, 7, 1), runner)
	RunDueBackups(context.Background(), at(10, 7, 2), runner)

	if len(runner.ran) != 1 {
		t.Fatalf("once, after the upgrade is closed: %v", runner.ran)
	}
	if len(flag.said) != 1 {
		t.Fatalf("a free box says nothing: %d", len(flag.said))
	}
}

// A flag that cannot be read might be an upgrade, and backups are taken tomorrow if not
// today. The log says which it was.
func TestBackupsWaitWhenTheBusyFileCannotBeRead(t *testing.T) {
	schedulerInTempDir(t)
	flag := upgradeFlagIn(t)
	flag.err = syscall.EACCES

	_ = SaveBackupSchedules([]BackupSchedule{daily(time.Time{})})

	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.ran) != 0 {
		t.Fatalf("an unreadable flag is not a free box: %v", runner.ran)
	}
	if len(flag.said) != 1 {
		t.Fatalf("one line: %d", len(flag.said))
	}
	if reason, ok := flag.field(0, "error"); !ok || !errors.Is(reason.Interface.(error), syscall.EACCES) {
		t.Fatalf("the line carries the reason: %+v", reason)
	}

	// nothing is spent either
	schedules, _ := BackupSchedules()
	if len(schedules) != 1 || !schedules[0].LastRun.IsZero() {
		t.Fatalf("%+v", schedules)
	}
}

// A tick is one cycle. A minute of waiting is one line however many backups wait, and the
// line says how many.
func TestAHeldTickSaysOneThingNoMatterHowManyWait(t *testing.T) {
	schedulerInTempDir(t)
	flag := upgradeFlagIn(t)
	flag.present = true

	_ = SaveBackupSchedules(threeDue())

	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(flag.said) != 1 {
		t.Fatalf("one line for the cycle, not one per backup: %d", len(flag.said))
	}
	if waiting, ok := flag.field(0, "waiting"); !ok || waiting.Integer != 3 {
		t.Fatalf("it says how many wait: %+v", waiting)
	}
	if len(runner.ran) != 0 {
		t.Fatalf("%v", runner.ran)
	}

	// and the next minute is another cycle
	RunDueBackups(context.Background(), at(10, 7, 1), runner)
	if len(flag.said) != 2 {
		t.Fatalf("a line per skipped cycle: %d", len(flag.said))
	}
}

func threeDue() []BackupSchedule {
	first, second, third := daily(time.Time{}), daily(time.Time{}), daily(time.Time{})
	first.App, second.App, third.App = "immich", "jellyfin", "nextcloud"

	return []BackupSchedule{first, second, third}
}

// openingRunner opens the upgrade while its first backup runs, the way one the owner
// starts can between two backups of a tick.
type openingRunner struct {
	fakeRunner
	flag *upgradeFlag
}

func (o *openingRunner) Run(ctx context.Context, schedule BackupSchedule, stamp string) (BackupManifest, error) {
	o.flag.present = true

	return o.fakeRunner.Run(ctx, schedule, stamp)
}

// A tick can hold hours of backups, so the flag is looked at before each of them, not
// once at the top.
func TestAnUpgradeThatOpensBetweenTwoBackupsHoldsTheSecond(t *testing.T) {
	schedulerInTempDir(t)
	flag := upgradeFlagIn(t)

	_ = SaveBackupSchedules(threeDue())

	runner := &openingRunner{flag: flag}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.ran) != 1 || !strings.HasPrefix(runner.ran[0], "immich@") {
		t.Fatalf("the first one was already running: %v", runner.ran)
	}
	if len(flag.said) != 1 {
		t.Fatalf("one line for the cycle: %d", len(flag.said))
	}
	if waiting, _ := flag.field(0, "waiting"); waiting.Integer != 2 {
		t.Fatalf("two are left waiting, not three: %+v", waiting)
	}

	// what ran is recorded as having run; what did not is still owed
	schedules, _ := BackupSchedules()
	for _, schedule := range schedules {
		if ran := !schedule.LastRun.IsZero(); ran != (schedule.App == "immich") {
			t.Fatalf("%s: recorded as run = %v", schedule.App, ran)
		}
	}

	flag.present = false
	catchUp := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 1), catchUp)
	if len(catchUp.ran) != 2 {
		t.Fatalf("the other two run once the upgrade is closed: %v", catchUp.ran)
	}
}

// A tick that finds nothing due costs one file read, and a box with nothing scheduled
// has nothing to say about an upgrade.
func TestATickWithNothingDueSaysNothingOfTheUpgrade(t *testing.T) {
	schedulerInTempDir(t)
	flag := upgradeFlagIn(t)
	flag.present = true

	RunDueBackups(context.Background(), at(10, 7, 0), &fakeRunner{})

	// ran at 03:00 today: nothing is due at 07:00
	_ = SaveBackupSchedules([]BackupSchedule{daily(at(10, 3, 0))})
	RunDueBackups(context.Background(), at(10, 7, 0), &fakeRunner{})

	if len(flag.said) != 0 || len(flag.asked) != 0 {
		t.Fatalf("said %d, asked %v", len(flag.said), flag.asked)
	}
}
