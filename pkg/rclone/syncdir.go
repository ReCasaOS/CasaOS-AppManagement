package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/backupprogress"
)

// Folder sync: the calls one pass of a scheduled sync is made of.

var (
	ErrTooManyDeletes = errors.New("too many files would be deleted")
	ErrSyncLocked     = errors.New("another pass holds the lock")
	ErrBisyncAborted  = errors.New("bisync aborted")
)

// SyncOptions says how one-way sync treats the destination.
type SyncOptions struct {
	Mirror    bool   // sync/sync instead of sync/copy
	BackupDir string // full fs ("/a/.b-archive/s" or "casaos-backup-x:p-archive/s"); "" = no archive
	MaxDelete int64  // read only when Mirror; -1 = no cap
}

// BisyncOptions says where a two-way pass keeps its state and what it archives.
type BisyncOptions struct {
	Workdir, BackupDir1, BackupDir2 string // empty BackupDir = no archive
	Resync, Force                   bool
}

// colours rclone puts around some of its errors.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// SyncDirectory copies srcFs into dstFs, or mirrors it (deleting at the destination what the
// source no longer has, up to MaxDelete files). Whatever is overwritten or deleted goes to BackupDir.
func (c *Client) SyncDirectory(ctx context.Context, srcFs, dstFs string, o SyncOptions) error {
	endpoint := "/sync/copy"
	config := map[string]any{}
	if o.BackupDir != "" {
		config["BackupDir"] = o.BackupDir
	}
	if o.Mirror {
		endpoint = "/sync/sync"
		if o.MaxDelete >= 0 {
			config["MaxDelete"] = o.MaxDelete
		}
	}

	params := map[string]string{"srcFs": srcFs, "dstFs": dstFs}
	if len(config) > 0 {
		encoded, _ := json.Marshal(config)
		params["_config"] = string(encoded)
	}

	err := c.runJob(ctx, endpoint, params)
	// rclone says this for a refused right too: only a capped mirror can have meant the cap
	if o.Mirror && o.MaxDelete >= 0 && err != nil && strings.Contains(ansi.ReplaceAllString(err.Error(), ""), "failed to delete") {
		return fmt.Errorf("%w: %w", ErrTooManyDeletes, err)
	}

	return err
}

// BisyncDirectory makes path1 and path2 alike in both directions. The safety options are not
// the caller's to drop: a pass that fails half way recovers, a deletion of more than half is
// refused, and a conflict keeps both versions.
func (c *Client) BisyncDirectory(ctx context.Context, path1, path2 string, o BisyncOptions) error {
	params := map[string]string{
		"path1": path1, "path2": path2,
		"resilient": "true", "recover": "true", "maxLock": "2m", "maxDelete": "50",
		"conflictResolve": "none", "createEmptySrcDirs": "true",
		"_config": `{"SuffixKeepExtension":true}`,
	}
	for key, value := range map[string]string{"workdir": o.Workdir, "backupDir1": o.BackupDir1, "backupDir2": o.BackupDir2} {
		if value != "" {
			params[key] = value
		}
	}
	if o.Resync {
		params["resync"] = "true"
	}
	if o.Force {
		params["force"] = "true"
	}

	err := c.runJob(ctx, "/sync/bisync", params)
	if err == nil {
		return nil
	}

	text := ansi.ReplaceAllString(err.Error(), "")
	switch {
	case strings.Contains(text, "too many deletes"):
		return fmt.Errorf("%w: %w", ErrTooManyDeletes, err)
	case strings.Contains(text, "prior lock file found"):
		return fmt.Errorf("%w: %w", ErrSyncLocked, err)
	case strings.Contains(text, "bisync aborted"):
		return fmt.Errorf("%w: %w", ErrBisyncAborted, err)
	}

	return err
}

func (c *Client) runJob(ctx context.Context, endpoint string, params map[string]string) error {
	id, err := c.startJob(endpoint, params)
	if err != nil {
		return err
	}

	err = c.waitForJob(ctx, id)
	// the job's last figures: a short job ends between two polls, and its figures are what the
	// pass says it moved. Only the syncs ask; the backups say what they said before.
	if backupprogress.Wanted(ctx) && ctx.Err() == nil {
		if stats, statsErr := c.jobStats(ctx, id); statsErr == nil {
			backupprogress.Report(ctx, stats)
		}
	}

	return err
}

// CountFiles is how many files lie under fs. A job, not a call: the listing is recursive, and
// a large cloud tree takes longer than the client waits for an answer.
func (c *Client) CountFiles(ctx context.Context, fs string) (int64, error) {
	id, err := c.startJob("/operations/size", map[string]string{"fs": fs})
	if err != nil {
		return 0, err
	}
	// a count is not the pass's progress: its figures are kept out of what the pass reports
	output, err := c.waitForJobOutput(backupprogress.With(ctx, func(backupprogress.Stats) {}), id)
	if err != nil {
		return 0, err
	}

	var size struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(output, &size); err != nil {
		return 0, fmt.Errorf("rclone counted the files without saying how many: %w", err)
	}

	return size.Count, nil
}

// IsEmpty says whether the top level of fs holds nothing at all.
func (c *Client) IsEmpty(fs string) (bool, error) {
	var listing struct {
		List []json.RawMessage `json:"list"`
	}
	if err := c.call("/operations/list", map[string]string{"fs": fs, "remote": ""}, &listing); err != nil {
		return false, err
	}

	return len(listing.List) == 0, nil
}

// Folders lists the folders directly under remote in fs, by name.
func (c *Client) Folders(fs, remote string) ([]string, error) {
	var listing struct {
		List []struct {
			Name  string `json:"Name"`
			IsDir bool   `json:"IsDir"`
		} `json:"list"`
	}
	err := c.call("/operations/list", map[string]string{"fs": fs, "remote": remote, "opt": `{"dirsOnly": true}`}, &listing)
	if err != nil {
		return nil, err
	}

	names := []string{}
	for _, entry := range listing.List {
		if entry.IsDir && entry.Name != "" {
			names = append(names, entry.Name)
		}
	}
	slices.Sort(names)

	return names, nil
}

// DestinationKind says which backend a destination is made of and whether it is encrypted.
// A destination with a root is an alias, an encrypted one a crypt: the backend is the one
// underneath, under the raw name.
func (c *Client) DestinationKind(name string) (backend string, encrypted bool, err error) {
	kind, err := c.remoteType(DestinationPrefix + name)
	if err != nil {
		return "", false, err
	}
	if kind != "alias" && kind != "crypt" {
		return kind, false, nil
	}

	backend, err = c.remoteType(DestinationPrefix + name + RawSuffix)

	return backend, kind == "crypt", err
}

func (c *Client) remoteType(remote string) (string, error) {
	var config struct {
		Type string `json:"type"`
	}
	if err := c.call("/config/get", map[string]string{"name": remote}, &config); err != nil {
		return "", err
	}

	return config.Type, nil
}
