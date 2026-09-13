package rclone

import (
	"context"
	"os"
	"path"
	"path/filepath"
)

// The other direction: from a destination back onto this host.

// RestoreDirectory makes hostPath hold what the destination holds under remotePath.
//
// sync/sync rather than sync/copy: a restore means "as it was", and a file that
// appeared in that folder after the backup was taken is not part of "as it was".
// This is the one call in the package that deletes something on this host, and
// it deletes exactly what a restore is asked to undo.
func (c *Client) RestoreDirectory(ctx context.Context, destinationName, remotePath, hostPath string) error {
	id, err := c.startJob("/sync/sync", map[string]string{
		"srcFs": Destination(destinationName, remotePath),
		"dstFs": hostPath,
	})
	if err != nil {
		return err
	}

	return c.waitForJob(ctx, id)
}

// RestoreFile puts one file from the destination at hostPath.
func (c *Client) RestoreFile(ctx context.Context, destinationName, remotePath, hostPath string) error {
	id, err := c.startJob("/operations/copyfile", map[string]string{
		"srcFs":     Destination(destinationName, path.Dir(remotePath)),
		"srcRemote": path.Base(remotePath),
		"dstFs":     path.Dir(hostPath),
		"dstRemote": path.Base(hostPath),
	})
	if err != nil {
		return err
	}

	return c.waitForJob(ctx, id)
}

// Fetch reads one file held at a destination.
//
// rclone's API has no "read this file" call; the file is copied into a folder
// of ours and read from there. Small files only -- a manifest, a compose file --
// which is all a restore needs to read before it starts copying.
func (c *Client) Fetch(ctx context.Context, destinationName, remotePath string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "casaos-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	local := filepath.Join(dir, path.Base(remotePath))
	if err := c.RestoreFile(ctx, destinationName, remotePath, filepath.ToSlash(local)); err != nil {
		return nil, err
	}

	return os.ReadFile(local)
}
