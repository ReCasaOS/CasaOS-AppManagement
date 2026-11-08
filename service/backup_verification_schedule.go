package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// The newest backup of each scheduled couple, read back every few days by itself. Off until
// somebody switches it on: a verification downloads the whole backup, which costs bandwidth
// and, at some providers, money.

var (
	ErrInvalidVerificationSchedule = errors.New("invalid verification schedule")
	VerificationSchedulePath       = "/var/lib/casaos/backups/verification-schedule.json"
)

// VerificationSchedule is when the tour of verifications goes off. At is local time, "HH:MM".
type VerificationSchedule struct {
	Enabled   bool      `json:"enabled"`
	EveryDays int       `json:"every_days"`
	At        string    `json:"at"`
	LastRun   time.Time `json:"last_run"`
}

var (
	// a variable so a test can count what is said
	logVerificationScheduleBroken     = logger.Error
	lastVerificationScheduleBrokenLog time.Time

	verificationScheduleLock sync.Mutex
	verificationTour         sync.Mutex // held by the tour that is running
)

// verificationWaitLimit bounds the wait for one verification: its own limit is an hour, and
// its transfer is given 30 seconds to stop after that.
const verificationWaitLimit = time.Hour + 5*time.Minute

// GetVerificationSchedule reads the schedule; a missing file is the default, switched off. A file
// that will not parse is an error: starting over would quietly switch it off.
func GetVerificationSchedule() (VerificationSchedule, error) {
	verificationScheduleLock.Lock()
	defer verificationScheduleLock.Unlock()

	return readVerificationSchedule()
}

func readVerificationSchedule() (VerificationSchedule, error) {
	v := VerificationSchedule{EveryDays: 7, At: "03:00"}
	content, err := os.ReadFile(VerificationSchedulePath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return v, nil
	}
	if err != nil {
		return VerificationSchedule{}, err
	}
	if err := stdjson.Unmarshal(content, &v); err != nil {
		return VerificationSchedule{}, fmt.Errorf("the verification schedule cannot be read; it was left untouched: %w", err)
	}

	return v, nil
}

func writeVerificationSchedule(v VerificationSchedule) error {
	encoded, err := stdjson.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(VerificationSchedulePath), 0o700); err != nil {
		return err
	}
	temp := VerificationSchedulePath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, VerificationSchedulePath)
}

// SetVerificationSchedule saves what the owner chose. The LastRun given is ignored: it is the
// box's to keep. Switching it on spends the slot already passed, so the first verification
// waits for the next one rather than starting the moment the switch is flipped.
func SetVerificationSchedule(v VerificationSchedule) error {
	if err := checkVerificationSchedule(v); err != nil {
		return err
	}

	verificationScheduleLock.Lock()
	defer verificationScheduleLock.Unlock()

	current, err := readVerificationSchedule()
	if err != nil {
		return err
	}

	v.LastRun = current.LastRun
	if v.Enabled && !current.Enabled {
		slot, err := verificationSlot(v, backupClock())
		if err != nil {
			return err
		}
		v.LastRun = slot
	}

	return writeVerificationSchedule(v)
}

// checkVerificationSchedule refuses what PUT refuses; a file edited by hand is held to it too.
func checkVerificationSchedule(v VerificationSchedule) error {
	if v.EveryDays < 1 || v.EveryDays > 30 {
		return fmt.Errorf("%w: every_days is 1 to 30, not %d", ErrInvalidVerificationSchedule, v.EveryDays)
	}
	if _, _, err := parseAt(v.At); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidVerificationSchedule, err)
	}

	return nil
}

// verificationSlot is today's time of day, or yesterday's when today's is still to come.
func verificationSlot(v VerificationSchedule, now time.Time) (time.Time, error) {
	return mostRecentSlot(BackupSchedule{Every: ScheduleDaily, At: v.At}, now)
}

// VerificationDue says whether the tour should go off: switched on, and at least EveryDays
// local calendar days between the day it last went off and the day of the slot just passed.
// Calendar days, not 24-hour blocks: the week the clocks change is still a week.
func VerificationDue(v VerificationSchedule, now time.Time) (bool, error) {
	if !v.Enabled {
		return false, nil
	}
	// every_days 0 would be due every minute, re-downloading every backup
	if err := checkVerificationSchedule(v); err != nil {
		return false, err
	}
	slot, err := verificationSlot(v, now)
	if err != nil {
		return false, err
	}
	if v.LastRun.IsZero() {
		return true, nil
	}

	day := func(t time.Time) time.Time {
		t = t.In(now.Location())
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	days := int(day(slot).Sub(day(v.LastRun)).Hours() / 24)

	return days >= v.EveryDays, nil
}

// RunDueVerifications verifies the newest successful backup of each enabled backup schedule,
// one after the other, when the schedule says so. One tour at a time; while an operating system
// upgrade is open it does nothing and records nothing, so the tour runs once it is closed (the
// backups' tick already says in the log that an upgrade holds things up). LastRun is the tour's
// `now`, written at the end whether every verification passed or not, as for the backups:
// the results are in the verification history.
func RunDueVerifications(ctx context.Context, now time.Time, client BackupVerificationClient) {
	if !verificationTour.TryLock() {
		return
	}
	defer verificationTour.Unlock()

	v, err := GetVerificationSchedule()
	if err != nil {
		logger.Error("could not read the verification schedule, so no verification ran", zap.Error(err))
		return
	}
	if due, err := VerificationDue(v, now); err != nil || !due {
		if err != nil && verificationBrokenNoticeDue() {
			logVerificationScheduleBroken("the verification schedule cannot be understood and will never run", zap.Error(err))
		}
		return
	}

	targets, err := verificationTargets()
	if err != nil {
		logger.Error("could not tell which backups to verify, so none was", zap.Error(err))
		return
	}

	for _, target := range targets {
		// asked before each one: a tour can last hours, and an upgrade can open meanwhile
		if open, _ := osUpgradeOpen(); open {
			return
		}
		if !verifyAndWait(ctx, target, client) {
			return // stopping: the tour is owed again
		}
	}

	if err := recordVerificationTour(now); err != nil {
		logger.Error("could not record when the verifications ran, so they may run again", zap.Error(err))
	}
}

// verificationBrokenNoticeDue lets a broken schedule be said once an hour, not every minute.
// Only the tour, which runs one at a time, calls it.
func verificationBrokenNoticeDue() bool {
	now := backupClock()
	if !lastVerificationScheduleBrokenLog.IsZero() && now.Sub(lastVerificationScheduleBrokenLog) < time.Hour {
		return false
	}
	lastVerificationScheduleBrokenLog = now

	return true
}

// verificationTargets is the newest successful backup of each couple an enabled backup
// schedule names, in a stable order. A couple kept with restic has its own repository check,
// and one with no successful backup has nothing to verify.
func verificationTargets() ([]BackupVerificationOptions, error) {
	schedules, err := BackupSchedules()
	if err != nil {
		return nil, err
	}
	runs, err := BackupRuns() // newest first
	if err != nil {
		return nil, err
	}

	targets := []BackupVerificationOptions{}
	seen := map[[2]string]bool{}
	for _, schedule := range schedules {
		couple := [2]string{schedule.App, schedule.Destination}
		if !schedule.Enabled || seen[couple] || resticHeld(schedule.Destination) {
			continue
		}
		seen[couple] = true
		for _, run := range runs {
			if !run.Restore && run.Succeeded() && run.App == schedule.App && run.Destination == schedule.Destination {
				targets = append(targets, BackupVerificationOptions{Destination: run.Destination, App: run.App, Stamp: run.Stamp})
				break
			}
		}
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].App != targets[j].App {
			return targets[i].App < targets[j].App
		}
		return targets[i].Destination < targets[j].Destination
	})

	return targets, nil
}

// verifyAndWait starts one verification and waits for it to end. A verification somebody
// started by hand holds the only slot: it is waited for, and this one tried once more. It
// says false when the tour must stop (the process is stopping).
func verifyAndWait(ctx context.Context, target BackupVerificationOptions, client BackupVerificationClient) bool {
	fields := []zap.Field{zap.String("app", target.App), zap.String("destination", target.Destination), zap.String("stamp", target.Stamp)}

	_, err := StartBackupVerification(target, client)
	if errors.Is(err, ErrBackupVerificationBusy) {
		if !waitVerification(ctx) {
			return false
		}
		_, err = StartBackupVerification(target, client)
	}
	if err != nil {
		logger.Error("a scheduled verification could not start, so it was skipped", append(fields, zap.Error(err))...)
		return true
	}

	return waitVerification(ctx)
}

// waitVerification waits for the verification running now, if any, to end; false when ctx
// ended first or it outlived every limit it has.
func waitVerification(ctx context.Context) bool {
	done := backupVerificationTasks.running()
	if done == nil {
		return true
	}

	timer := time.NewTimer(verificationWaitLimit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		logger.Error("a verification outlived its time limit; the scheduled tour stopped waiting for it")
		return false
	}
}

// running is the channel closed when the verification running now ends, or nil when none runs.
func (m *backupVerificationManager) running() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeID == "" {
		return nil
	}

	return m.done
}

// recordVerificationTour writes the tour down in the file as it is now: the owner may have
// changed the schedule while the tour ran.
func recordVerificationTour(now time.Time) error {
	verificationScheduleLock.Lock()
	defer verificationScheduleLock.Unlock()

	v, err := readVerificationSchedule()
	if err != nil {
		return err
	}
	v.LastRun = now

	return writeVerificationSchedule(v)
}
