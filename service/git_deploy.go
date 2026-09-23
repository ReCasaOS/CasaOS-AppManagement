package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// A deployment of a git app: the target commit is built beside the running version,
// switched in, judged, and rolled back when it does not start. The running app is not
// touched until the build has succeeded.

type gitTrigger int

const (
	gitTriggerManual gitTrigger = iota
	gitTriggerAutomatic
	// gitTriggerRestore is a restore's deployment of the backed-up commit: by hand, and built
	// wherever the commit's tag went since.
	gitTriggerRestore
)

// gitBuildLogCap is how much of the last build's log is kept.
const gitBuildLogCap = 1 << 20

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DeployGitApp deploys commit, or the remote's latest when commit is empty (for an app that
// follows tags, the highest eligible tag), in the background. A commit of the history is a
// revert. env is written as .env first.
func DeployGitApp(ctx context.Context, name, commit string, env *string) (*GitAppView, error) {
	return startGitDeploy(ctx, name, commit, "", env, gitTriggerManual)
}

// DeployGitAppTag deploys tag, one of the eligible tags of an app that follows tags, lower ones
// included, or the highest when tag is empty, in the background. A tag whose commit ran is a
// revert. env is written as .env first.
func DeployGitAppTag(ctx context.Context, name, tag string, env *string) (*GitAppView, error) {
	return startGitDeploy(ctx, name, "", tag, env, gitTriggerManual)
}

// startGitDeploy checks what must hold, records the operation and starts the deployment,
// and returns the app as the deployment starts. The guard is released when it ends.
func startGitDeploy(ctx context.Context, name, commit, tag string, env *string, trigger gitTrigger) (*GitAppView, error) {
	if commit != "" && !gitCommitPattern.MatchString(commit) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a full commit hash", commit))
	}

	end, err := Begin(name, gitOperationDeploy)
	if err != nil {
		return nil, err
	}

	return deployHolding(ctx, name, commit, tag, env, trigger, end)
}

// deployHolding is startGitDeploy for a caller that holds the app already: end is released
// when the deployment ends, or before returning when it does not start. The automatic
// deployment gives the commit and the tag its check saw; by hand, an app that follows tags is
// given a tag, a commit of its history, or neither for the highest tag.
func deployHolding(ctx context.Context, name, commit, tag string, env *string, trigger gitTrigger, end func()) (*GitAppView, error) {
	handedOff := false
	defer func() {
		if !handedOff {
			end()
		}
	}()

	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if st.Origin == gitOriginAdoptable {
		if err := adoptGitApp(ctx, st); err != nil {
			return nil, err
		}
	}

	revert := commit != "" && st.historyEntry(commit) != nil
	target := commit
	switch {
	case !st.followsTags() && tag != "":
		return nil, GitRequestError(fmt.Sprintf("%s follows a branch: deploy a commit, not a tag", st.App))
	case !st.followsTags() && target == "":
		// the branch's head, which runs under no tag
		auth, err := gitAuth(st)
		if err != nil {
			return nil, err
		}
		if target, err = git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
			return nil, GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
		}
	case !st.followsTags() && trigger == gitTriggerManual:
		// a commit given by hand runs as it ran: a version a tag brought stays one, whatever
		// the branch holds, until the branch's head is deployed
		if st.Deployed != nil && target == st.Deployed.Commit {
			tag = st.Deployed.Tag
		} else if entry := st.historyEntry(target); entry != nil {
			tag = entry.Tag
		}
	case st.followsTags() && target == "":
		// by hand: the tag asked for, or the highest, as the remote has it now
		chosen, err := gitTagToDeploy(ctx, st, tag)
		if err != nil {
			return nil, err
		}
		target, tag = chosen.Commit, chosen.Name
		// a tag whose commit ran before is a revert: its images are there. The running
		// version is none: it is deployed again only as a repair
		revert = gitRan(st.historyEntry(target)) && (st.Deployed == nil || target != st.Deployed.Commit)
	case st.followsTags() && tag == "":
		// by hand, a commit: the running version, deployed again under its tag as a repair,
		// or a version of the history that ran, under the tag it ran as
		entry := st.historyEntry(target)
		switch {
		case st.Deployed != nil && target == st.Deployed.Commit:
			tag, revert = st.Deployed.Tag, false
		case gitRan(entry):
			tag = entry.Tag
		default:
			return nil, GitRequestError(fmt.Sprintf("%s is no version of the history that ran: %s follows tags, deploy one of them", target[:12], st.App))
		}
	}

	if err := gitDeployPreconditions(ctx, st, target, tag, env, revert); err != nil {
		if trigger == gitTriggerAutomatic {
			// nothing changes but the reason, which the dashboard is told, and the commit is
			// not tried again
			failGitDeploy(common.WithProperties(ctx, map[string]string{common.PropertyTypeAppName.Name: st.App}), st, target, tag, "", nil, gitOutcomeFailed, err)
		}

		return nil, err
	}
	// the deployed commit gets past the preconditions only as a repair: its version deployed
	// again, which is no revert
	redeploy := st.Deployed != nil && target == st.Deployed.Commit
	revert = revert && !redeploy

	if env != nil {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile([]byte(*env)); err != nil {
			return nil, err
		}
	}

	kind := gitOperationBuild
	switch {
	case revert:
		kind = gitOperationRevert
	case redeploy && gitDocker.ImagesExist(ctx, st.Deployed.Images):
		kind = gitOperationDeploy
	}
	st.Operation = &gitOperation{Kind: kind, Commit: target, Tag: tag, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	// read before the deployment runs: it changes st from here on
	view := newGitAppView(ctx, st)

	handedOff = true
	go func() {
		defer end()
		runGitDeploy(context.Background(), st, target, tag, revert, trigger)
	}()

	return view, nil
}

// gitTagToDeploy is tag among the eligible tags of the remote, the highest when tag is empty:
// what a deployment by hand of an app that follows tags goes to.
func gitTagToDeploy(ctx context.Context, st *gitApp, tag string) (GitTag, error) {
	auth, err := gitAuth(st)
	if err != nil {
		return GitTag{}, err
	}

	chosen, err := eligibleGitTag(ctx, st, auth, tag)
	if err != nil && !errors.As(err, new(GitRequestError)) {
		err = GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
	}

	return chosen, err
}

// gitDeployPreconditions is what must hold before anything is touched.
func gitDeployPreconditions(ctx context.Context, st *gitApp, target, tag string, env *string, revert bool) error {
	if !st.Cloned {
		return GitRequestError(fmt.Sprintf("%s is not cloned yet: check it first", st.App))
	}

	info, err := git.Describe(ctx, st.Dir)
	switch {
	case err != nil:
		return GitRequestError(fmt.Sprintf("%s is not a git work tree any more: %v", st.Dir, err))
	case info.Head == "":
		return GitRequestError(fmt.Sprintf("%s holds no commit any more", st.Dir))
	case info.Branch != st.Branch && st.followsTags():
		return GitRequestError(fmt.Sprintf("%s is on the branch %s: an app that follows tags deploys from a detached HEAD", st.Dir, info.Branch))
	case info.Branch != st.Branch:
		return GitRequestError(fmt.Sprintf("%s is not on the branch %s", st.Dir, st.Branch))
	case !info.TrackedFilesClean:
		return GitRequestError(fmt.Sprintf("tracked files were modified in %s: commit or discard those changes first", st.Dir))
	}

	if env != nil {
		if st.Deployed != nil {
			return GitRequestError(".env is written with the first deployment only; change it from the app's .env settings")
		}
		if tracked, _ := git.IsTracked(ctx, st.Dir, ".env"); tracked {
			return GitRequestError("the repository tracks .env, and CasaOS never writes a tracked file")
		}
		if _, err := ParseEnvFile([]byte(*env)); err != nil {
			return GitRequestError(err.Error())
		}
	}

	if st.Deployed != nil && target == st.Deployed.Commit && (st.Blocked || info.Head != target || tag != st.Deployed.Tag) {
		// a repair: the version that runs, deployed again over a rollback that failed, a folder
		// an interrupted revert moved, or under another tag (or none, back on a branch), puts
		// the folder back on its commit. On a branch that commit must contain whatever the
		// folder is on; tags do not form a line
		if st.followsTags() {
			return nil
		}
		if err := gitFolderContainedIn(ctx, st.Dir, target, target); err != nil {
			return GitRequestError(err.Error())
		}

		return nil
	}
	if st.Deployed != nil && !revert && target == st.Deployed.Commit {
		// a build tags the running version's own images: a rollback would find the new ones
		return GitRequestError(fmt.Sprintf("%s is already deployed: there is nothing new to build", target[:12]))
	}
	if st.Deployed != nil && revert && info.Head != st.Deployed.Commit {
		return GitRequestError(fmt.Sprintf("%s is on %s, not on the deployed %s: a revert would drop the commits made there", st.Dir, info.Head[:12], st.Deployed.Commit[:12]))
	}
	if revert && !gitRevertable(ctx, st, *st.historyEntry(target)) {
		return GitRequestError(fmt.Sprintf("%s cannot be reverted to: it is the running version, it never ran, or its images or its commit are gone", target[:12]))
	}

	return nil
}

// runGitDeploy carries the deployment out and records how it ended.
func runGitDeploy(ctx context.Context, st *gitApp, target, tag string, revert bool, trigger gitTrigger) {
	ctx = common.WithProperties(ctx, map[string]string{common.PropertyTypeAppName.Name: st.App})
	previous := st.Deployed
	// the deployed commit again, never a revert: an app repaired, or restored at the version its
	// state names
	redeploy := previous != nil && target == previous.Commit

	auth, err := gitAuth(st)
	if err != nil {
		failGitDeploy(ctx, st, target, tag, "", nil, gitOutcomeFailed, err)
		return
	}

	var images map[string]string
	switch {
	case revert:
		images = st.historyEntry(target).Images
	case redeploy && gitDocker.ImagesExist(ctx, previous.Images):
		// nothing to build: what that version ran is still there
		images = previous.Images
	default:
		// for a redeployment, only once its images are gone: a build tags them anew
		if images, err = fetchAndBuild(ctx, st, target, tag, previous, trigger, auth); err != nil {
			return
		}

		st.Operation.Kind = gitOperationDeploy
		if err := saveGitApp(st); err != nil {
			logger.Error("the deployment could not be written down", zap.Error(err), zap.String("app", st.App))
		}
		// once the state says deploying: the dashboard reads the app again when it hears this
		go PublishEventWrapper(ctx, common.EventTypeAppGitBuildEnd, nil)
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	move := git.FastForward
	if previous == nil || revert || redeploy || st.followsTags() || previous.Tag != "" {
		// tags do not form a line: an app that follows them is moved, never fast-forwarded,
		// and so is the first deployment on a branch after them, or the first of all, whose
		// folder may be on a tag the app was cloned at. fetchGitBranch found target on the branch
		move = git.ResetKeep
	}
	if err := move(ctx, st.Dir, target); err != nil {
		// nothing moved: the folder and the running version are as they were
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed, err)
		return
	}

	err = gitDocker.Retag(ctx, st.App, st.Dir, target)
	if err == nil {
		err = gitDocker.Start(ctx, st.App, st.Dir)
	}
	if err != nil {
		rollBackGitDeploy(ctx, st, target, tag, subject, images, previous, err)
		return
	}

	st.Deployed = &gitDeployment{Commit: target, Tag: tag, Subject: subject, At: time.Now().UTC(), Images: images}
	st.Blocked = false
	if trigger != gitTriggerAutomatic && !redeploy {
		// going down pauses the automatic rebuild, or the next check would undo it: on a branch
		// a revert, or an older tag; for tags, an older tag only, so that the same or a higher
		// one deployed by hand, a revert included, resumes it. The first deployment of an app
		// that follows tags, no tag deployed before it, pauses nothing. The version that runs,
		// deployed again, leaves the switch as it was
		previousTag := ""
		if previous != nil {
			previousTag = previous.Tag
		}
		st.AutoPaused = (revert && !st.followsTags()) || gitTagHigher(previousTag, tag)
	}
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")
	finishGitDeploy(ctx, st, gitHistoryEntry{Commit: target, Tag: tag, Subject: subject, At: st.Deployed.At, Outcome: gitOutcomeDeployed, Images: images})

	go PublishEventWrapper(ctx, common.EventTypeAppGitDeployEnd, nil)
}

// fetchAndBuild fetches what target comes from and checks it may be deployed, then builds
// it: on a branch, target is on it and descends from what runs; for tags, the tag still
// names target. A failure is recorded here.
func fetchAndBuild(ctx context.Context, st *gitApp, target, tag string, previous *gitDeployment, trigger gitTrigger, auth git.Auth) (map[string]string, error) {
	var err error
	switch {
	case !st.followsTags():
		err = fetchGitBranch(ctx, st, target, previous, auth)
	case trigger != gitTriggerRestore && (previous == nil || target != previous.Commit):
		// a repair builds the commit that runs, and a restore the backed-up commit, which the
		// folder holds, wherever their tag went
		err = fetchGitTag(ctx, st, target, tag, auth)
	}
	if err != nil {
		failGitDeploy(ctx, st, target, tag, "", nil, gitOutcomeFailed, err)
		return nil, err
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	images, err := buildGitCommit(ctx, st, target, auth)
	if err != nil {
		failGitDeploy(ctx, st, target, tag, subject, nil, gitOutcomeBuildFailed, err)
		return nil, err
	}

	return images, nil
}

// fetchGitBranch fetches the branch, and checks target is on it, descends from what runs, and
// contains whatever the folder is on. The first deployment on the branch after tags descends
// from nothing: the tag that ran may come from another line.
func fetchGitBranch(ctx context.Context, st *gitApp, target string, previous *gitDeployment, auth git.Auth) error {
	head, err := git.Fetch(ctx, st.Dir, st.Remote, st.Branch, auth)
	if err != nil {
		return err
	}

	onBranch, err := git.IsAncestor(ctx, st.Dir, target, head)
	if err == nil && !onBranch {
		err = fmt.Errorf("%s is not on the branch %s", target[:12], st.Branch)
	}
	if err == nil && previous != nil && previous.Tag == "" {
		var descends bool
		if descends, err = git.IsAncestor(ctx, st.Dir, previous.Commit, target); err == nil && !descends {
			err = fmt.Errorf("%s does not descend from the deployed %s", target[:12], previous.Commit[:12])
		}
	}
	if err == nil && previous != nil {
		err = gitFolderContainedIn(ctx, st.Dir, previous.Commit, target)
	}

	return err
}

// fetchGitTag fetches the tag alone and checks it still names target, the commit the check or
// the request saw. None of a branch's rules holds: tags do not form a line.
func fetchGitTag(ctx context.Context, st *gitApp, target, tag string, auth git.Auth) error {
	fetched, err := git.FetchTag(ctx, st.Dir, st.Remote, tag, auth)
	if err == nil && fetched != target {
		err = fmt.Errorf("the tag %s moved since the check, from %.12s to %.12s: check again", tag, target, fetched)
	}

	return err
}

// gitFolderContainedIn refuses a folder holding commits target does not contain, made there
// by hand: a rollback moves the folder back to deployed, and would drop them.
func gitFolderContainedIn(ctx context.Context, dir, deployed, target string) error {
	info, err := git.Describe(ctx, dir)
	if err != nil || info.Head == deployed {
		return err
	}

	contained, err := git.IsAncestor(ctx, dir, info.Head, target)
	if err == nil && !contained {
		err = fmt.Errorf("%s is on %s, which %s does not contain: push the commits made there, or move the folder back to %s", dir, info.Head[:12], target[:12], deployed[:12])
	}

	return err
}

// buildGitCommit builds commit in a worktree of its own, with the build's output going to
// the app's build log and to the bus. The log is closed, its last lines sent, before it
// returns: the caller announces how the build ended once that is written down.
func buildGitCommit(ctx context.Context, st *gitApp, commit string, auth git.Auth) (map[string]string, error) {
	file, err := os.OpenFile(gitAppFile(st.App, ".build.log"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	out := &gitBuildLog{ctx: ctx, file: file}

	// before any line of the build: the dashboard clears its log when it hears this
	PublishEventWrapper(ctx, common.EventTypeAppGitBuildBegin, nil)

	images, err := func() (map[string]string, error) {
		worktree := filepath.Join(gitAppsDir, "work", st.App+"-"+commit[:12])
		// removed even when adding it failed half-way, as a submodule that cannot be fetched
		// leaves it: the next attempt of this commit needs the path free
		defer func() {
			if err := git.RemoveWorktree(context.Background(), st.Dir, worktree); err != nil {
				logger.Error("a build's worktree could not be removed", zap.Error(err), zap.String("path", worktree))
			}
		}()
		if err := git.AddWorktree(ctx, st.Dir, commit, worktree, auth); err != nil {
			return nil, err
		}

		return gitDocker.Build(ctx, st.App, worktree, filepath.Join(st.Dir, ".env"), commit, out)
	}()
	if err != nil {
		fmt.Fprintf(out, "\nthe build of %s failed: %v\n", commit[:12], err)
	}
	if closeErr := out.Close(); closeErr != nil {
		logger.Error("the build log could not be closed", zap.Error(closeErr), zap.String("app", st.App))
	}

	return images, err
}

// rollBackGitDeploy puts the previous version back after target did not start: its
// commit, its images, and a start. A created app's first version has nothing before it
// and stays stopped with its log.
func rollBackGitDeploy(ctx context.Context, st *gitApp, target, tag, subject string, images map[string]string, previous *gitDeployment, cause error) {
	if previous == nil {
		if err := gitDocker.Stop(ctx, st.App); err != nil {
			logger.Error("a first version that did not start could not be stopped", zap.Error(err), zap.String("app", st.App))
		}
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed, cause)

		return
	}

	err := git.ResetKeep(ctx, st.Dir, previous.Commit)
	if err == nil {
		err = gitDocker.Retag(ctx, st.App, st.Dir, previous.Commit)
	}
	if err == nil {
		err = gitDocker.Start(ctx, st.App, st.Dir)
	}

	if err != nil {
		st.Blocked = true
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed,
			fmt.Errorf("%w; the rollback to %s failed too: %v", cause, previous.Commit[:12], err))

		return
	}

	st.Blocked = false
	failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeRolledBack, cause)
}

// failGitDeploy records a deployment that did not end with target running, and says so.
func failGitDeploy(ctx context.Context, st *gitApp, target, tag, subject string, images map[string]string, outcome string, cause error) {
	st.attempt(target)
	finishGitDeploy(ctx, st, gitHistoryEntry{
		Commit: target, Tag: tag, Subject: subject, At: time.Now().UTC(), Outcome: outcome, Reason: cause.Error(), Images: images,
	})

	// after the state is written down: the dashboard reads the app again when it hears this
	event := common.EventTypeAppGitDeployError
	if outcome == gitOutcomeBuildFailed {
		event = common.EventTypeAppGitBuildError
	}
	go PublishEventWrapper(ctx, event, map[string]string{common.PropertyTypeMessage.Name: cause.Error()})
}

// finishGitDeploy records how a deployment ended, clears the operation, removes the images
// no remembered version names any more, and keeps the commits the history offers.
func finishGitDeploy(ctx context.Context, st *gitApp, entry gitHistoryEntry) {
	st.Operation = nil
	if removable := st.record(entry); len(removable) > 0 {
		gitDocker.RemoveImages(ctx, removable)
	}
	keepGitCommits(ctx, st)

	if err := saveGitApp(st); err != nil {
		logger.Error("the end of a deployment could not be written down", zap.Error(err), zap.String("app", st.App))
	}
}

// keepGitCommits keeps a ref in the folder for the running version and every version of the
// history that ran, and drops the others: git never collects a commit a revert may go back
// to, whatever became of the tag or the branch that brought it.
func keepGitCommits(ctx context.Context, st *gitApp) {
	if !st.Cloned {
		return
	}

	wanted := map[string]bool{}
	if st.Deployed != nil {
		wanted[st.Deployed.Commit] = true
	}
	for i := range st.History {
		if gitRan(&st.History[i]) {
			wanted[st.History[i].Commit] = true
		}
	}

	kept, err := git.KeptCommits(ctx, st.Dir)
	if err != nil {
		logger.Error("the commits a git app keeps could not be listed", zap.Error(err), zap.String("app", st.App))
		return
	}
	for _, commit := range kept {
		if wanted[commit] {
			delete(wanted, commit)
		} else if err := git.DropKeptCommit(ctx, st.Dir, commit); err != nil {
			logger.Error("a commit a git app kept could not be dropped", zap.Error(err), zap.String("app", st.App))
		}
	}
	for commit := range wanted {
		if err := git.KeepCommit(ctx, st.Dir, commit); err != nil {
			logger.Error("a commit of a git app could not be kept", zap.Error(err), zap.String("app", st.App))
		}
	}
}

// deployGitAppAutomatically starts the deployment of what the check holding the app saw,
// when the app's automatic rebuild may act on it. The check's hold passes to the
// deployment under the deployment's name, so nothing starts in between and what is refused
// meanwhile is told a deployment runs; end is released either way.
func deployGitAppAutomatically(ctx context.Context, name string, end func()) {
	st, err := loadGitApp(name)
	if err != nil || !gitMayDeployAutomatically(st) {
		end()
		return
	}

	handOver(name, gitOperationDeploy)
	if _, err := deployHolding(ctx, name, st.Check.RemoteCommit, st.Check.RemoteTag, nil, gitTriggerAutomatic, end); err != nil {
		logger.Info("no automatic deployment", zap.String("app", name), zap.Error(err))
	}
}

// gitMayDeployAutomatically reports whether the automatic rebuild may deploy what the last
// check saw: a commit other than the running one and never tried, on an app neither paused
// nor blocked. An app that follows tags only ever goes up, from a tag deployed in that mode:
// a deleted, a lower or a moved tag deploys nothing. A branch app acts once a version of its
// branch runs, never over one a tag deployed.
func gitMayDeployAutomatically(st *gitApp) bool {
	if !st.AutoDeploy || st.AutoPaused || st.Blocked || st.Deployed == nil || st.Check == nil || st.Check.Error != "" ||
		st.Check.RemoteCommit == "" || st.Check.RemoteCommit == st.Deployed.Commit || lo.Contains(st.Attempted, st.Check.RemoteCommit) {
		return false
	}
	if st.followsTags() {
		return st.Deployed.Tag != "" && gitTagHigher(st.Check.RemoteTag, st.Deployed.Tag)
	}

	return st.Deployed.Tag == ""
}

// gitBuildLog is a build's output: written to the app's build log, whose last megabyte is
// kept, and sent to the bus a batch of lines at most every second.
type gitBuildLog struct {
	mu       sync.Mutex
	ctx      context.Context
	file     *os.File
	written  int64
	pending  bytes.Buffer
	lastSent time.Time
}

func (l *gitBuildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// a log that cannot be written is not a build that failed
	if n, err := l.file.Write(p); err == nil {
		l.written += int64(n)
	}
	if l.written > 2*gitBuildLogCap {
		l.keepTail()
	}

	l.pending.Write(p)
	if time.Since(l.lastSent) >= time.Second {
		l.send(false)
	}

	return len(p), nil
}

// keepTail cuts the log down to its last megabyte.
func (l *gitBuildLog) keepTail() {
	tail := make([]byte, gitBuildLogCap)
	n, _ := l.file.ReadAt(tail, l.written-gitBuildLogCap)
	if err := l.file.Truncate(0); err != nil {
		return
	}
	n, _ = l.file.WriteAt(tail[:n], 0)
	l.written = int64(n)
	_, _ = l.file.Seek(l.written, 0)
}

// send publishes the complete lines written since the last time, or everything at the end.
func (l *gitBuildLog) send(everything bool) {
	text := l.pending.String()
	if !everything {
		text = text[:strings.LastIndexByte(text, '\n')+1]
	}
	if text == "" {
		return
	}

	l.pending.Next(len(text))
	l.lastSent = time.Now()
	PublishEventWrapper(l.ctx, common.EventTypeAppGitBuildProgress, map[string]string{common.PropertyTypeMessage.Name: text})
}

func (l *gitBuildLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.send(true)

	return l.file.Close()
}
