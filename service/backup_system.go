package service

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Backing up the box itself.
//
// Apps can be put back on a machine that has nothing. The machine's own state
// could not: its users, its shares and their accounts, its schedules, the
// destinations it sends backups to. This is that state, treated as one more
// thing a destination can hold, under a name no app is likely to take.

// SystemBackupName is the app name the box's own backups are filed under.
const SystemBackupName = "casaos-system"

// systemPaths is what the box is, apart from its apps. Each is an entry the
// plan treats like a bind mount: a folder or a file on this host, put back at
// the same place. Absent ones are skipped with the reason, not planned.
var systemPaths = []string{
	"/etc/casaos",
	"/etc/samba/smb.casa.conf",
	"/var/lib/casaos/db",
	"/var/lib/casaos/conf",
	rcloneConfigPath,
	// the dashboard's API widgets, with the keys they call their APIs with; the
	// core names this file in pkg/apiwidgets (ConfigFile), and the two must agree
	apiWidgetsPath,
}

const (
	rcloneConfigPath = "/root/.config/rclone/rclone.conf"
	apiWidgetsPath   = "/var/lib/casaos/api_widgets.json"
)

// systemSecretFiles hold keys, at paths that do not follow the data folder. A
// destination does not always give a file back with the mode it had, so a restore
// that cannot leave these two to root only fails and says so.
var systemSecretFiles = []string{rcloneConfigPath, apiWidgetsPath}

// systemUnits are the services that hold the files above open. Stopped for the
// copy in both directions -- sqlite files copied under a running writer are the
// same bad backup a database in a container would be -- and started again
// after. Not app-management, which is doing the copying, nor the gateway or
// the bus, which hold none of them.
var systemUnits = []string{"casaos", "casaos-user-service", "casaos-local-storage"}

// rcloneUnit holds the one file a restore changes under the daemon's feet.
const rcloneUnit = "rclone"

// gatewayUnit serves the certificates a restore may have replaced. In practice
// only the certificates and keys under tls/ live in the data folder: the routes,
// the hosts and the guard's configuration are in the gateway's runtime path.
const gatewayUnit = "casaos-gateway"

// servedByGateway is whether a restored target is the gateway's (the certificates
// and keys under tls/, and whatever it keeps in its own folder), so that it must
// be restarted.
func servedByGateway(target string) bool {
	for _, folder := range []string{"tls", "gateway"} {
		if dir := path.Join(systemDataRoot, folder); target == dir || strings.HasPrefix(target, dir+"/") {
			return true
		}
	}

	return false
}

// unitControl starts and stops services; a seam so a test does not touch systemd.
type unitControl interface {
	Stop(unit string) error
	Start(unit string) error
	Restart(unit string) error
}

type systemdControl struct{}

func (systemdControl) Stop(unit string) error  { return exec.Command("systemctl", "stop", unit).Run() }
func (systemdControl) Start(unit string) error { return exec.Command("systemctl", "start", unit).Run() }
func (systemdControl) Restart(unit string) error {
	return exec.Command("systemctl", "restart", unit).Run()
}

// Systemd is the real thing.
func Systemd() unitControl { return systemdControl{} }

// systemBackupPaths is systemPaths, the backup schedules, the folders left out of backups and
// the verification schedule, whose files are variables (the schedules' follows the data path),
// so they are read at the moment of the backup, not fixed.
func systemBackupPaths() []string {
	return append(slices.Clone(systemPaths), BackupSchedulePath, BackupExclusionsPath, VerificationSchedulePath)
}

// isSymlink is whether p is a symbolic link on this box; a seam so a test needs no
// real filesystem. A path that does not exist is not one.
var isSymlink = func(p string) bool {
	info, err := os.Lstat(p)

	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// underSymlink is whether target, below the data folder, is a symbolic link or has
// one for a parent: the copy would write where the link leads, outside the box.
func underSymlink(target string) bool {
	root := path.Clean(systemDataRoot)
	rel, under := strings.CutPrefix(target, root+"/")
	if !under {
		return false
	}
	p := root
	for _, part := range strings.Split(rel, "/") {
		p = path.Join(p, part)
		if isSymlink(p) {
			return true
		}
	}

	return false
}

// tightened is whether a restored target holds a secret the copy may have
// recreated with the default modes: the data folder (the keys of tls/ and fleet/)
// and the secret files, wherever the data folder is.
func tightened(target string) bool {
	return slices.Contains(systemSecretFiles, target) || strings.HasPrefix(target, path.Clean(systemDataRoot)+"/")
}

// tightenRestored takes the group and other permissions off p and all it holds.
// p itself may be a link (root's rclone.conf kept elsewhere): the copy wrote
// through it, so what it leads to is tightened. A link below p is not followed.
// It goes on past a failure and returns the first.
var tightenRestored = func(p string) error {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	var first error
	_ = filepath.WalkDir(p, func(q string, d iofs.DirEntry, err error) error {
		if err == nil && d.Type()&os.ModeSymlink == 0 {
			var info iofs.FileInfo
			if info, err = d.Info(); err == nil && info.Mode().Perm()&0o077 != 0 {
				err = os.Chmod(q, info.Mode().Perm()&^0o077)
			}
		}
		if err != nil && first == nil {
			first = err
		}

		return nil
	})

	return first
}

func pathExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// PlanSystemBackup is the plan for the box, worked out from what is here now.
func PlanSystemBackup(fs systemFS) (BackupManifest, error) {
	entries, err := SystemBackupInventory(fs)
	if err != nil {
		return BackupManifest{}, err
	}
	manifest := PlanBackup(SystemBackupName, entries, nil)
	manifest.MarkBindFiles(func(p string) bool {
		info, err := os.Stat(p)

		return err == nil && info.Mode().IsRegular()
	})

	return manifest, nil
}

// SystemBackupOptions is one backup of the box.
type SystemBackupOptions struct {
	Destination string
	Stamp       string
	// HoldStill stops the services that hold the files open for the copy.
	HoldStill bool
}

// RunSystemBackup copies the box's own state to a destination.
func RunSystemBackup(ctx context.Context, copier BackupCopier, units unitControl, opts SystemBackupOptions) (BackupManifest, error) {
	if opts.Destination == "" {
		return BackupManifest{}, errors.New("a backup needs a destination")
	}
	if opts.Stamp == "" {
		return BackupManifest{}, errors.New("a backup needs a name to file it under")
	}

	// before anything is stopped: a box that cannot be read is no backup
	manifest, err := PlanSystemBackup(realSystemFS())
	if err != nil {
		return BackupManifest{}, err
	}

	// it holds services, never an app, so Begin does not see it: see markInProgress
	defer markInProgress(SystemBackupName, "backup")()

	ctx = backupEventContext(ctx, &ComposeApp{Name: SystemBackupName}, opts.Destination, opts.Stamp, "backup")
	ctx, endLive := beginLive(ctx, SystemBackupName, opts.Destination, opts.Stamp)
	defer endLive()
	publishBackupBegin(ctx)

	manifest.ContainersStopped = opts.HoldStill

	err = func() error {
		if opts.HoldStill {
			release, err := holdUnits(units, systemUnits)
			defer release()
			if err != nil {
				return err
			}
		}

		return copyPlan(ctx, &manifest, copier, opts.Destination, RootFor(SystemBackupName, opts.Stamp))
	}()
	err = stoppedFromInterface(ctx, err)
	if err != nil {
		publishBackupError(ctx, err)
		return manifest, err
	}
	publishBackupEnd(ctx)

	return manifest, nil
}

// holdUnits stops units in order and returns what starts them again in reverse,
// every one attempted, the way holdApp does for containers.
func holdUnits(units unitControl, names []string) (release func(), err error) {
	stopped := []string{}
	release = func() {
		for i := len(stopped) - 1; i >= 0; i-- {
			if err := units.Start(stopped[i]); err != nil {
				logger.Error("a service did not start again after the copy", zap.Error(err), zap.String("unit", stopped[i]))
			}
		}
	}

	for _, name := range names {
		if err := units.Stop(name); err != nil {
			return release, fmt.Errorf("could not stop %s, so the copy did not run: %w", name, err)
		}
		stopped = append(stopped, name)
	}

	return release, nil
}

// SystemBackupOnDemand runs one backup of the box and records it.
func SystemBackupOnDemand(ctx context.Context, copier BackupCopier, units unitControl, opts SystemBackupOptions) (BackupManifest, error) {
	record := BackupRunRecord{
		App: SystemBackupName, Destination: opts.Destination, Stamp: opts.Stamp,
		StartedAt: time.Now(), Scheduled: false, ContainersStopped: opts.HoldStill,
	}

	manifest, err := RunSystemBackup(ctx, copier, units, opts)
	finishBackupRecord(&record, manifest, err)

	return manifest, err
}

// SystemRestoreOptions is one restore of the box.
type SystemRestoreOptions struct {
	Destination string
	Stamp       string
}

// RestoreSystemBackup puts the box's own state back the way the backup had it.
//
// The manifest says what to put back and this box says where it may go: each
// operation is restored to its target, in the manifest's order, provided the
// target is part of the box (systemRestorable) and is not, nor lies under, a
// symbolic link; one that is not, whatever the manifest claims, is written
// nowhere and reported as missing. A folder comes back as a mirror: what the box
// has there and the backup has not is deleted. What came back has its group and
// other permissions taken off (the keys travel with it); a secret file that
// cannot be left to root only fails the restore. The services
// holding the files are stopped for the copy and started again whatever happens;
// rclone is restarted last, after everything has been copied through it, if its
// configuration came back, and then the gateway, if its certificates or its own
// files did.
func RestoreSystemBackup(ctx context.Context, restorer BackupRestorer, units unitControl, opts SystemRestoreOptions) (RestoreReport, error) {
	report := RestoreReport{Restored: []BackupOperation{}, Missing: []BackupOperation{}, Skipped: []BackupEntry{}}
	if opts.Destination == "" {
		return report, ErrRestoreNeedsDestination
	}
	if opts.Stamp == "" {
		return report, ErrRestoreNeedsStamp
	}

	defer markInProgress(SystemBackupName, "restore")()

	root := RootFor(SystemBackupName, opts.Stamp)
	manifest, err := fetchManifest(ctx, restorer, opts.Destination, root)
	if err != nil {
		return report, err
	}
	if manifest.App != SystemBackupName {
		return report, fmt.Errorf("the backup at %s is of `%s`, not of this box", root, manifest.App)
	}
	report.Skipped = append(report.Skipped, manifest.Skipped...)

	ctx = backupEventContext(ctx, &ComposeApp{Name: SystemBackupName}, opts.Destination, opts.Stamp, "restore")
	publishBackupBegin(ctx)

	rcloneTouched, gatewayTouched := false, false
	err = func() error {
		release, err := holdUnits(units, systemUnits)
		defer release()
		if err != nil {
			return err
		}

		for i, operation := range manifest.Operations {
			if !systemRestorable(operation.Target) || underSymlink(operation.Target) {
				report.Missing = append(report.Missing, operation)

				continue
			}
			publishBackupProgress(ctx, i, len(manifest.Operations), operation.Destination)

			// this box's path, not the one the manifest's Source names
			operation.Source = operation.Target
			remote := path.Join(root, operation.Destination)
			if operation.Directory {
				err = restorer.RestoreDirectory(ctx, opts.Destination, remote, operation.Source)
			} else {
				err = restorer.RestoreFile(ctx, opts.Destination, remote, operation.Source)
			}
			// the copy brings private keys back, maybe with default modes: tightened at
			// once, a half-done copy included, so before any service starts again
			if tightened(operation.Source) {
				if tightenErr := tightenRestored(operation.Source); tightenErr != nil {
					logger.Error("the permissions of a restored target could not be tightened", zap.Error(tightenErr), zap.String("target", operation.Source))
					// the data folder may sit on a disk that keeps no modes, so that is
					// only written down; the secret files are on the system's own disk
					if err == nil && slices.Contains(systemSecretFiles, operation.Source) {
						err = fmt.Errorf("it came back, and could not be made readable by root only: %w", tightenErr)
					}
				}
			}
			if err != nil {
				return fmt.Errorf("restoring %s: %w", operation.Source, err)
			}
			switch {
			case operation.Source == rcloneConfigPath:
				rcloneTouched = true
			case servedByGateway(operation.Source):
				gatewayTouched = true
			}

			report.Restored = append(report.Restored, operation)
		}

		return nil
	}()
	if err != nil {
		publishBackupError(ctx, err)
		return report, err
	}

	// Last, once nothing more goes through the daemon: it reads its configuration
	// at start, and the file under it has just been replaced.
	if rcloneTouched {
		if err := units.Restart(rcloneUnit); err != nil {
			logger.Error("rclone did not restart after its configuration was put back", zap.Error(err))
		}
	}

	publishBackupEnd(ctx)

	// After everything else: it reads its certificates and routes at start, and
	// writes back from memory only guard-state.json, which a backup leaves out.
	if gatewayTouched {
		if err := units.Restart(gatewayUnit); err != nil {
			logger.Error("the gateway did not restart after its files were put back", zap.Error(err))
		}
	}

	return report, nil
}

// SystemRestoreOnDemand runs one restore of the box and records it.
func SystemRestoreOnDemand(ctx context.Context, restorer BackupRestorer, units unitControl, opts SystemRestoreOptions) (RestoreReport, error) {
	record := BackupRunRecord{
		App: SystemBackupName, Destination: opts.Destination, Stamp: opts.Stamp,
		StartedAt: time.Now(), Scheduled: false, ContainersStopped: true, Restore: true,
	}

	report, err := RestoreSystemBackup(ctx, restorer, units, opts)

	record.FinishedAt = time.Now()
	record.Copied = len(report.Restored)
	record.SkippedCount = len(report.Missing)
	if err != nil {
		record.Error = err.Error()
	}
	if logErr := RecordBackupRun(record); logErr != nil {
		logger.Error("a restore ran and could not be written down", zap.Error(logErr), zap.String("app", SystemBackupName))
	}

	return report, err
}
