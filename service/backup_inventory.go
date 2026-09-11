package service

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// What a backup of an app is made of, decided before anything is copied.
//
// Deciding it up front, as data, is what makes the rest possible: the same answer
// tells a download what to stream, tells rclone what to send, and tells the person
// asking what they are about to get and what they are not. A backup that silently
// leaves something out is discovered on the day it is restored.

// BackupKind says why a path is in the list, which is also what a restore needs to
// know to put it back.
type BackupKind string

const (
	// BackupKindCompose is the app's docker-compose.yml
	BackupKindCompose BackupKind = "compose"
	// BackupKindEnv is the .env beside it
	BackupKindEnv BackupKind = "env"
	// BackupKindVolume is a named volume, which Docker keeps somewhere of its own
	BackupKindVolume BackupKind = "volume"
	// BackupKindBind is a host path a service mounts directly
	BackupKindBind BackupKind = "bind"
)

// BackupEntry is one thing on disk that belongs to an app -- or one that looked
// like it did and does not, which is worth saying rather than dropping.
type BackupEntry struct {
	Kind BackupKind `json:"kind"`
	// Path on the host. Empty for an entry that was skipped before it could be
	// resolved, and for a named volume until Docker has been asked where it lives.
	Path string `json:"path"`
	// Service that mounts it, empty for the app's own files
	Service string `json:"service,omitempty"`
	// Target inside the container, for a volume or a bind
	Target string `json:"target,omitempty"`
	// Volume name as the compose file writes it, for a named volume
	Volume string `json:"volume,omitempty"`
	// Skip is why this is not going in the backup. Empty means it is.
	Skip string `json:"skip,omitempty"`
}

// IsIncluded reports whether this entry is part of the backup.
func (e BackupEntry) IsIncluded() bool { return e.Skip == "" && e.Path != "" }

// hostPathSkipReason says why a host path is not this app's data.
//
// A compose file mounts more than data. The Docker socket is how a container is
// given control of the daemon, `/dev`, `/sys` and `/proc` are kernel interfaces
// with no contents to copy, and the host clock is the host's. Copying any of them
// is at best noise; restoring them onto another machine is how a backup does
// damage on the day it is finally used.
func hostPathSkipReason(hostPath string) string {
	clean := path.Clean(hostPath)

	switch clean {
	case "", ".", "/":
		return "the whole filesystem is not an app's data"
	case "/var/run/docker.sock", "/run/docker.sock":
		return "the Docker socket is a door, not data"
	case "/etc/localtime", "/etc/timezone":
		return "the host's clock belongs to the host"
	}

	for _, kernel := range []string{"/dev", "/sys", "/proc"} {
		if clean == kernel || strings.HasPrefix(clean, kernel+"/") {
			return "kernel interface, with no contents to copy"
		}
	}

	return ""
}

// resolveBindSource turns a bind's source into an absolute host path.
//
// Compose resolves a relative source against the directory holding the compose
// file, not against the working directory of whoever is asking -- so `./backend`
// in an app's file means that app's folder, wherever this code happens to run.
func resolveBindSource(source, composeDir string) string {
	if source == "" {
		return ""
	}

	if strings.HasPrefix(source, "/") {
		return path.Clean(source)
	}

	// `~` is the shell's, and the daemon has no home to expand it against
	if strings.HasPrefix(source, "~") {
		return ""
	}

	return path.Clean(path.Join(composeDir, source))
}

// backupEntriesForServices lists what an app's services mount, in a stable order.
//
// It answers without asking Docker anything, so it can say what a backup WOULD
// contain before one is run. A named volume's entry carries the name rather than a
// path: only the daemon knows where it put it, and that is a separate question
// from which volumes belong to this app.
func backupEntriesForServices(services types.Services, composeDir string) []BackupEntry {
	entries := []BackupEntry{}

	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, volume := range services[name].Volumes {
			entries = append(entries, backupEntryForVolume(volume, name, composeDir))
		}
	}

	return entries
}

func backupEntryForVolume(volume types.ServiceVolumeConfig, serviceName, composeDir string) BackupEntry {
	switch volume.Type {
	case types.VolumeTypeVolume:
		if volume.Source == "" {
			// An anonymous volume: Docker made it, nothing names it, and nothing can
			// find it again after a restore. Saying so beats a silent omission.
			return BackupEntry{
				Kind: BackupKindVolume, Service: serviceName, Target: volume.Target,
				Skip: "an anonymous volume has no name to restore it under",
			}
		}

		return BackupEntry{
			Kind: BackupKindVolume, Service: serviceName,
			Target: volume.Target, Volume: volume.Source,
		}

	case types.VolumeTypeTmpfs:
		return BackupEntry{
			Kind: BackupKindVolume, Service: serviceName, Target: volume.Target,
			Skip: "tmpfs lives in memory and is empty at every start",
		}

	case types.VolumeTypeNamedPipe, types.VolumeTypeCluster:
		return BackupEntry{
			Kind: BackupKindBind, Service: serviceName, Target: volume.Target,
			Skip: volume.Type + " is not a folder to copy",
		}
	}

	// Anything else is a bind, including an entry whose type the compose file left
	// out -- which is what the short syntax produces.
	resolved := resolveBindSource(volume.Source, composeDir)
	if resolved == "" {
		return BackupEntry{
			Kind: BackupKindBind, Service: serviceName, Target: volume.Target,
			Skip: "this mount has no host path that can be resolved",
		}
	}

	if reason := hostPathSkipReason(resolved); reason != "" {
		return BackupEntry{
			Kind: BackupKindBind, Service: serviceName, Target: volume.Target,
			Path: resolved, Skip: reason,
		}
	}

	return BackupEntry{
		Kind: BackupKindBind, Service: serviceName,
		Target: volume.Target, Path: resolved,
	}
}

// BackupInventory is everything on disk that belongs to this app, decided without
// copying anything and without asking Docker.
//
// It lists what is left OUT as well as what goes in, each with its reason. A
// backup that quietly drops a mount is discovered on the day it is restored, which
// is the worst possible day to discover it.
//
// A named volume comes back carrying its name and no path: where Docker put it is
// the daemon's answer, asked separately, and the list of which volumes belong to
// this app does not depend on the daemon being reachable.
func (a *ComposeApp) BackupInventory() []BackupEntry {
	entries := []BackupEntry{}

	if len(a.ComposeFiles) > 0 {
		composeFile := path.Clean(filepath.ToSlash(a.ComposeFiles[0]))
		entries = append(entries, BackupEntry{Kind: BackupKindCompose, Path: composeFile})

		// The `.env` beside it is not optional detail: an app whose compose file
		// references ${...} is unusable without it, and it is the one file in a
		// backup that holds secrets. EnvFile() already answers where it is.
		entries = append(entries, BackupEntry{
			Kind: BackupKindEnv,
			Path: path.Clean(filepath.ToSlash(a.EnvFile())),
		})
	}

	return append(entries, backupEntriesForServices(a.Services, a.composeDir())...)
}

// composeDir is the folder a relative bind source resolves against: compose uses
// the project's working directory, which for an installed app is the folder its
// compose file sits in.
func (a *ComposeApp) composeDir() string {
	if a.WorkingDir != "" {
		return path.Clean(filepath.ToSlash(a.WorkingDir))
	}

	if len(a.ComposeFiles) == 0 {
		return ""
	}

	return path.Dir(path.Clean(filepath.ToSlash(a.ComposeFiles[0])))
}

// DockerVolumeName is the name Docker actually knows a compose volume by.
//
// A compose file says `backend-storage`; Docker stores it as `<project>_backend-storage`
// unless the file declares a name of its own or marks it external. compose-go has
// already worked that out at load time, so the answer is read rather than rebuilt
// -- rebuilding it is how a backup addresses a volume that does not exist and
// reports success having copied nothing.
func (a *ComposeApp) DockerVolumeName(composeName string) string {
	config, ok := a.Volumes[composeName]
	if !ok || config.Name == "" {
		return composeName
	}

	return config.Name
}
