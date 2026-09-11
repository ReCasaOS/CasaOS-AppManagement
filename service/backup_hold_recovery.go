package service

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Getting an app back when the process that stopped it did not survive to start
// it again.
//
// holdApp defers the release, which covers every way a backup can fail -- except
// the one where nothing runs at all: a kill, a crash, a power cut, an upgrade that
// restarts the service mid-copy. The containers are down, the code that knew about
// them is gone, and nothing on the box is looking. The app is simply off, and
// stays off until somebody notices.
//
// So the intention is written down before the first container is stopped, and
// erased after the last one is started. A file still there at startup is an app
// somebody's backup left behind.

// BackupHoldPath records which apps are being held still, and which containers
// were running when they were.
var BackupHoldPath = "/var/lib/casaos/backups/holding.json"

var backupHoldLock sync.Mutex

type heldRecord struct {
	Containers []string  `json:"containers"`
	Since      time.Time `json:"since"`
}

func readHeld() (map[string]heldRecord, error) {
	content, err := os.ReadFile(BackupHoldPath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return map[string]heldRecord{}, nil
	}
	if err != nil {
		return nil, err
	}

	held := map[string]heldRecord{}
	if err := stdjson.Unmarshal(content, &held); err != nil {
		return nil, err
	}

	return held, nil
}

func writeHeld(held map[string]heldRecord) error {
	encoded, err := stdjson.MarshalIndent(held, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(BackupHoldPath), 0o700); err != nil {
		return err
	}

	temp := BackupHoldPath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, BackupHoldPath)
}

// rememberHeld writes down what is about to be stopped.
//
// Written BEFORE the first stop, not after the last: a crash between the two is
// exactly the case this exists for, and a note written afterwards would not be
// there yet.
func rememberHeld(app string, containers []string, now time.Time) error {
	if app == "" || len(containers) == 0 {
		return nil
	}

	backupHoldLock.Lock()
	defer backupHoldLock.Unlock()

	held, err := readHeld()
	if err != nil {
		return err
	}

	held[app] = heldRecord{Containers: containers, Since: now}

	return writeHeld(held)
}

// forgetHeld erases the note once the app is back.
func forgetHeld(app string) error {
	backupHoldLock.Lock()
	defer backupHoldLock.Unlock()

	held, err := readHeld()
	if err != nil {
		return err
	}

	if _, ok := held[app]; !ok {
		return nil
	}

	delete(held, app)

	return writeHeld(held)
}

// RecoverHeldApps starts back anything a backup stopped and never restarted.
//
// Run at startup. Starting a container that is already running is what
// StartContainer does nothing about, so this is safe to run when the note is
// merely stale -- and a stale note is a far better problem than an app nobody
// noticed was off.
func RecoverHeldApps(docker containerStopStarter) {
	backupHoldLock.Lock()
	held, err := readHeld()
	backupHoldLock.Unlock()

	if err != nil {
		logger.Error("could not read which apps a backup was holding", zap.Error(err))
		return
	}

	apps := make([]string, 0, len(held))
	for app := range held {
		apps = append(apps, app)
	}
	sort.Strings(apps)

	for _, app := range apps {
		record := held[app]
		logger.Error("an app was left stopped by a backup that did not finish; starting it again",
			zap.String("app", app), zap.Time("since", record.Since), zap.Int("containers", len(record.Containers)))

		// In reverse, the way a release does, so a stack whose services depend on
		// each other comes back the way it went down.
		recovered := true
		for i := len(record.Containers) - 1; i >= 0; i-- {
			if err := docker.StartContainer(record.Containers[i]); err != nil {
				recovered = false
				logger.Error("could not start a container back",
					zap.Error(err), zap.String("app", app), zap.String("container", record.Containers[i]))
			}
		}

		// The note survives a failure, so the next startup tries again. A daemon
		// that was not up yet is the likely reason, and clearing the note there
		// would leave the app off with nothing left pointing at it. The cost of
		// keeping it is an error in the log at every boot until somebody looks,
		// which is the point.
		if !recovered {
			continue
		}

		if err := forgetHeld(app); err != nil {
			logger.Error("could not clear the hold note", zap.Error(err), zap.String("app", app))
		}
	}
}
