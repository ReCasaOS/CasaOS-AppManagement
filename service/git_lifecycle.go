package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"go.uber.org/zap"
)

// Git apps across the life of the process: the periodic check, and what a stop in the
// middle of an operation left behind.

// CheckGitApps checks every registered git app, one after another, and starts what the
// automatic rebuild may deploy. An app busy with another operation is checked next time.
func CheckGitApps(ctx context.Context) {
	names, err := GitAppNames()
	if err != nil {
		logger.Error("cannot list the git apps to check", zap.Error(err))
		return
	}

	for _, name := range names {
		st, end, err := beginGitCheck(ctx, name)
		if err != nil {
			if !errors.As(err, new(ErrAppBusy)) {
				logger.Error("cannot check a git app", zap.Error(err), zap.String("app", name))
			}
			continue
		}

		checkGitApp(ctx, st, false)
		deployGitAppAutomatically(ctx, name, end)
	}
}

// RecoverGitApps runs once, at startup. An operation the process did not live to finish is
// recorded as interrupted and cleared, and nothing is retried: the commit joins those the
// automatic rebuild leaves alone. The worktrees of builds and the clones cut short are
// removed.
func RecoverGitApps(ctx context.Context) {
	names, err := GitAppNames()
	if err != nil {
		logger.Error("cannot list the git apps to recover", zap.Error(err))
		return
	}

	for _, name := range names {
		st, err := loadGitApp(name)
		if err != nil {
			logger.Error("cannot read a git app", zap.Error(err), zap.String("app", name))
			continue
		}

		if st.Origin == gitOriginCreated && !st.Cloned {
			_ = os.RemoveAll(gitPartialClone(st))
		}

		if st.Operation == nil {
			continue
		}

		operation := st.Operation
		st.Operation = nil
		if operation.Kind != gitOperationCheck {
			subject := ""
			if st.Cloned && operation.Commit != "" {
				subject, _ = git.Subject(ctx, st.Dir, operation.Commit)
			}
			st.attempt(operation.Commit)
			removable := st.record(gitHistoryEntry{
				Commit: operation.Commit, Tag: operation.Tag, Subject: subject, At: time.Now().UTC(), Outcome: gitOutcomeInterrupted,
				Reason: fmt.Sprintf("AppManagement stopped during the %s started at %s", operation.Kind, operation.StartedAt.Format(time.RFC3339)),
				// what the build may have tagged already, so that the history's cleanup reaches it
				Images: interruptedGitImages(ctx, st, operation.Commit),
			})
			if len(removable) > 0 {
				gitDocker.RemoveImages(ctx, removable)
			}
			keepGitCommits(ctx, st)
		}

		if err := saveGitApp(st); err != nil {
			logger.Error("cannot record an interrupted operation", zap.Error(err), zap.String("app", name))
		}
	}

	work := filepath.Join(gitAppsDir, "work")
	entries, _ := os.ReadDir(work)
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(work, entry.Name())); err != nil {
			logger.Error("cannot remove a build's worktree", zap.Error(err), zap.String("path", entry.Name()))
		}
	}

	for _, name := range names {
		if st, err := loadGitApp(name); err == nil && st.Cloned {
			if err := git.Prune(ctx, st.Dir); err != nil {
				logger.Error("cannot prune the worktrees of a git app", zap.Error(err), zap.String("app", name))
			}
		}
	}
}

// interruptedGitImages are the git- tags a build of commit makes, read from the compose files
// in the folder: those an interrupted build may have made already.
func interruptedGitImages(ctx context.Context, st *gitApp, commit string) map[string]string {
	images := map[string]string{}
	if !st.Cloned || !gitCommitPattern.MatchString(commit) {
		return images
	}

	project, err := loadGitProject(ctx, st.App, st.Dir, filepath.Join(st.Dir, ".env"))
	if err != nil {
		return images
	}
	for _, name := range builtServiceNames(project.Services) {
		if tag, err := gitImageTag(api.GetImageNameOrDefault(project.Services[name], st.App), commit); err == nil {
			images[name] = tag
		}
	}

	return images
}
