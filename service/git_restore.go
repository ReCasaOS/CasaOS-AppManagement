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
// never deployed. The tag fields are there for an app that follows tags only: a branch app's
// origin is what older versions wrote.
func gitBackupOrigin(app string) *BackupGit {
	st, err := loadGitApp(app)
	if err != nil || st.Deployed == nil {
		return nil
	}

	origin := &BackupGit{Remote: st.Remote, Branch: st.Branch, Commit: st.Deployed.Commit}
	if st.followsTags() {
		origin.Follow, origin.Tag, origin.TagPattern, origin.Prereleases = gitFollowTags, st.Deployed.Tag, st.TagPattern, st.Prereleases
	}

	return origin
}

// restoreGitApp is installGitAppFromBackup; a var so a restore's test replaces it.
var restoreGitApp = installGitAppFromBackup

// installGitAppFromBackup puts a git app back on a machine that does not have it: it is
// registered with the backup's remote and branch, or its mode and tag filter, cloned at the
// backed-up commit, given the backup's .env, then built and started, while the restore holds
// it. An app that follows tags is cloned at the backup's tag (see gitRestoreRef), detached,
// and the commit fetched by its hash when the clone does not hold it. A repository that
// cannot be reached leaves the app registered with the access it needs.
func installGitAppFromBackup(ctx context.Context, name string, origin BackupGit, env []byte) (*ComposeApp, error) {
	// a manifest is a file anyone with the destination's credentials can edit. A tag-mode
	// manifest may name no tag: taken before the first deployment in that mode
	byTags := origin.Follow == gitFollowTags
	switch {
	case !gitCommitPattern.MatchString(origin.Commit):
		return nil, fmt.Errorf("the backup's commit `%s` is not a full commit hash", origin.Commit)
	case gitURLKind(origin.Remote) == "":
		return nil, fmt.Errorf("the backup's remote `%s` is not a URL CasaOS can clone", redactGitURL(origin.Remote))
	case !validGitBranch(origin.Branch):
		return nil, fmt.Errorf("the backup's branch `%s` is not a branch name", origin.Branch)
	case origin.Follow != "" && checkGitFollow(origin.Follow) != nil:
		return nil, fmt.Errorf("the backup's follow `%s` is neither branch nor tags", origin.Follow)
	case byTags && !validGitBranch(origin.Tag):
		return nil, fmt.Errorf("the backup's tag `%s` is not a tag name", origin.Tag)
	case checkGitTagPattern(origin.TagPattern) != nil:
		return nil, fmt.Errorf("the backup's tag pattern `%s` is not one", origin.TagPattern)
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
			Follow: gitFollowBranch, TagPattern: origin.TagPattern, Prereleases: origin.Prereleases,
			History: []gitHistoryEntry{}, Attempted: []string{},
		}
		if byTags {
			st.Follow, st.Branch = gitFollowTags, ""
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
	ref, tag := st.Branch, ""
	if st.followsTags() {
		if ref, err = gitRestoreRef(ctx, st, origin.Tag, auth); err != nil {
			return nil, err
		}
		tag = origin.Tag
	} else if _, err := git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
		return nil, gitAccessRequired(st, err)
	}

	if !st.Cloned {
		if err := cloneGitApp(ctx, st, ref, auth); err != nil {
			return nil, err
		}
		// cloned at the default branch when no tag was left: an app that follows tags deploys
		// from a detached HEAD
		if st.followsTags() {
			if err := git.Detach(ctx, st.Dir); err != nil {
				return nil, err
			}
		}
		if err := saveGitApp(st); err != nil {
			return nil, err
		}
	}

	if !git.HasCommit(ctx, st.Dir, origin.Commit) {
		if !st.followsTags() {
			return nil, fmt.Errorf("the backed-up commit %s is not on %s any more", origin.Commit[:12], st.Branch)
		}
		// the tag moved or went: the commit itself, from a forge that gives it, and never
		// another in its place
		err := git.FetchCommit(ctx, st.Dir, st.Remote, origin.Commit, auth)
		if err == nil && !git.HasCommit(ctx, st.Dir, origin.Commit) {
			err = errors.New("the fetch did not bring it")
		}
		if err != nil && origin.Tag == "" {
			return nil, fmt.Errorf("the backup names no tag for its commit, and the forge does not give that commit: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("the tag %s no longer points at the backed-up commit, and the forge does not give that commit: %w", origin.Tag, err)
		}
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

	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: origin.Commit, Tag: tag, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}
	runGitDeploy(ctx, st, origin.Commit, tag, false, gitTriggerRestore)
	if st.Deployed == nil || st.Deployed.Commit != origin.Commit {
		return nil, fmt.Errorf("`%s` could not be deployed at %s: %s", name, origin.Commit[:12], st.History[0].Reason)
	}

	files, err := gitComposeFiles(st.Dir)
	if err != nil {
		return nil, err
	}

	return LoadComposeAppFromConfigFile(name, strings.Join(files, ","))
}

// gitRestoreRef is what a restore clones an app that follows tags at: the backup's tag while
// the remote has it (an empty tag never is: no listed tag has an empty name), else the
// highest eligible tag, else the remote's default branch. The backed-up commit is fetched
// afterwards when that clone does not hold it. A remote that cannot be reached asks for
// access.
func gitRestoreRef(ctx context.Context, st *gitApp, tag string, auth git.Auth) (string, error) {
	tags, err := git.LsRemoteTags(ctx, st.Remote, auth)
	if err != nil {
		return "", gitAccessRequired(st, err)
	}
	if tags[tag] != "" {
		return tag, nil
	}
	if eligible := eligibleGitTags(tags, st.TagPattern, st.Prereleases); len(eligible) > 0 {
		return eligible[0].Name, nil
	}

	branch, err := git.DefaultBranch(ctx, st.Remote, auth)
	if err == nil && (branch == "" || !validGitBranch(branch)) {
		err = fmt.Errorf("`%s` is not a branch name", branch)
	}
	if err != nil {
		return "", fmt.Errorf("the remote has no tag to clone the repository at, and its default branch cannot be read: %w", err)
	}

	return branch, nil
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
