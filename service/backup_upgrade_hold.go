package service

import (
	"errors"
	"io/fs"
	"os"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

// Backups and an operating system upgrade of the box do not mix: dpkg rewrites the
// system underneath a copy, and the containers a backup holds still are stopped and
// started by the upgrade as well.
//
// While an upgrade is open the core keeps this file, from the click that starts it
// until it is closed, which includes the days a finished run can wait for its restart.
// Only the core writes it, and it is read here for its existence alone: asking whether
// it is there needs no more than the right to look into its directory, which a service
// that is not root has.

// osUpgradeBusyPath is the file; the core and this service agree on the name. A variable
// so a test can look at a file of its own with everything else as production wires it.
var osUpgradeBusyPath = "/var/lib/casaos/os-upgrade/busy"

// osUpgradeBusyStat and logBackupsHeld are variables so a test can answer with an error
// a temporary directory cannot give (a denied directory, a failing disk) and count what
// was said. The file is looked at, not what it points to: a link whose target is gone is
// still something put there, and the core reads it the same way.
var (
	osUpgradeBusyStat = os.Lstat
	logBackupsHeld    = logger.Warn
)

// osUpgradeOpen says whether backups must stay out of the way.
//
// Only a file that is not there means no upgrade. Any other answer is an upgrade nobody
// can rule out, and is told as one: a backup that waits a day costs less than one taken
// through the middle of an upgrade. The error, when there is one, is why the answer is
// yes although nothing says an upgrade is open.
func osUpgradeOpen() (open bool, err error) {
	_, err = osUpgradeBusyStat(osUpgradeBusyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	return true, err
}
