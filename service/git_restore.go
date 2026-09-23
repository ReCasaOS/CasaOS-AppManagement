package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
)

// A git app in a backup: its origin goes in the manifest, and a restore on a machine that
// does not have the app clones and builds it from there. A backup holds no key and no
// token, so a private repository needs access again.

// gitBackupOrigin is what a backup records of a git app, nil for any other app and for one
// never deployed.
func gitBackupOrigin(app string) *BackupGit {
	st, err := loadGitApp(app)
	if err != nil || st.Deployed == nil {
		return nil
	}

	return &BackupGit{Remote: st.Remote, Branch: st.Branch, Commit: st.Deployed.Commit}
}

// restoreGitApp is installGitAppFromBackup; a var so a restore's test replaces it.
var restoreGitApp = installGitAppFromBackup

// installGitAppFromBackup puts a git app back on a machine that does not have it: it is
// registered with the backup's remote and branch, cloned at the backed-up commit, given
// the backup's .env, then built and started, while the restore holds it. A repository that
// cannot be reached leaves the app registered with the access it needs.
func installGitAppFromBackup(ctx context.Context, name string, origin BackupGit, env []byte) (*ComposeApp, error) {
	// a manifest is a file anyone with the destination's credentials can edit
	switch {
	case !gitCommitPattern.MatchString(origin.Commit):
		return nil, fmt.Errorf("the backup's commit `%s` is not a full commit hash", origin.Commit)
	case gitURLKind(origin.Remote) == "":
		return nil, fmt.Errorf("the backup's remote `%s` is not a URL CasaOS can clone", redactGitURL(origin.Remote))
	case !validGitBranch(origin.Branch):
		return nil, fmt.Errorf("the backup's branch `%s` is not a branch name", origin.Branch)
	}

	// a backup holds no webhook secret: whatever this machine kept, the app comes back with
	// its webhook off, turned off before anything is registered so that no delivery signed
	// with an older secret is taken meanwhile
	off := false
	if err := setGitWebhook(name, &off, false); err != nil {
		return nil, err
	}

	st, err := loadGitApp(name)
	if errors.Is(err, ErrGitAppNotFound) {
		st = &gitApp{
			App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
			Remote: origin.Remote, Branch: origin.Branch, Access: git.AccessNone,
			History: []gitHistoryEntry{}, Attempted: []string{},
		}
		err = saveGitApp(st)
	}
	if err != nil {
		return nil, err
	}

	auth, err := gitAuth(st)
	if err != nil {
		return nil, err
	}
	if _, err := git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
		return nil, gitAccessRequired(st, err)
	}

	if !st.Cloned {
		if err := cloneGitApp(ctx, st, st.Branch, auth); err != nil {
			return nil, err
		}
		if err := saveGitApp(st); err != nil {
			return nil, err
		}
	}

	if !git.HasCommit(ctx, st.Dir, origin.Commit) {
		return nil, fmt.Errorf("the backed-up commit %s is not on %s any more", origin.Commit[:12], st.Branch)
	}
	if err := git.ResetKeep(ctx, st.Dir, origin.Commit); err != nil {
		return nil, err
	}
	// a repository that tracks .env restores its own, and CasaOS never writes a tracked file
	if tracked, _ := git.IsTracked(ctx, st.Dir, ".env"); len(env) > 0 && !tracked {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile(env); err != nil {
			return nil, err
		}
	}

	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: origin.Commit, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}
	runGitDeploy(ctx, st, origin.Commit, false, gitTriggerManual)
	if st.Deployed == nil || st.Deployed.Commit != origin.Commit {
		return nil, fmt.Errorf("`%s` could not be deployed at %s: %s", name, origin.Commit[:12], st.History[0].Reason)
	}

	files, err := gitComposeFiles(st.Dir)
	if err != nil {
		return nil, err
	}

	return LoadComposeAppFromConfigFile(name, strings.Join(files, ","))
}

// gitAccessRequired prepares the access a restore could not reach the repository without,
// and says what the owner has to do: add a new deploy key for an SSH remote, give a token
// for any other.
func gitAccessRequired(st *gitApp, cause error) error {
	if gitURLKind(st.Remote) != "ssh" {
		return fmt.Errorf("access required: give `%s` a token in its Repository tab, then restore again (%v)", st.App, cause)
	}

	if st.Access != git.AccessKey {
		if err := setGitAccess(st, git.AccessKey, ""); err != nil {
			return err
		}
		if err := saveGitApp(st); err != nil {
			return err
		}
	}

	public, err := os.ReadFile(gitAppFile(st.App, ".key.pub"))
	if err != nil {
		return err
	}

	return fmt.Errorf("access required: add this deploy key to %s as a read-only key, then restore again: %s (%v)",
		st.Remote, strings.TrimSpace(string(public)), cause)
}
