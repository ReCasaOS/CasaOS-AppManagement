package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// Registering, adopting, changing and checking apps deployed from their git repository.

// GitRequestError is a request that cannot be carried out as it is; the API answers 400
// with its message, which says what to change.
type GitRequestError string

func (e GitRequestError) Error() string { return string(e) }

var (
	// ErrGitAppNameTaken is answered 409.
	ErrGitAppNameTaken = errors.New("the name is already used by an app, a compose project or an app deployed from git")
	// ErrGitAppDeployed is answered 409: a deployed app goes with its uninstall.
	ErrGitAppDeployed = errors.New("the app has been deployed: uninstall it instead")
)

// GitAppRegistration is an app to create from a repository.
type GitAppRegistration struct {
	Name   string
	URL    string
	Branch string
	Access string
	Token  string
}

// GitAppChanges changes a git app; nil leaves a field as it is.
type GitAppChanges struct {
	Branch     *string
	AutoDeploy *bool
	Access     *string
	Token      *string
}

// checkGitAccess refuses an access mode the URL cannot carry. hasToken says whether a
// token is given or already kept.
func checkGitAccess(remote, access string, hasToken bool) error {
	kind := gitURLKind(remote)

	switch {
	case access != git.AccessNone && access != git.AccessKey && access != git.AccessToken:
		return GitRequestError(fmt.Sprintf("access is none, key or token, not `%s`", access))
	case kind == "":
		return GitRequestError(fmt.Sprintf("`%s` is not a URL CasaOS can clone: HTTPS, SSH or file, without a password in it (a password is a token)", redactGitURL(remote)))
	case access == git.AccessKey && kind != "ssh":
		return GitRequestError("a deploy key works over SSH: give the SSH URL of the repository")
	case access == git.AccessToken && kind != "http":
		return GitRequestError("a token works over HTTPS: give the HTTPS URL of the repository")
	case access == git.AccessToken && !hasToken:
		return GitRequestError("access by token needs the token")
	}

	return nil
}

// setGitAccess changes how the app reaches its remote: a key is generated when it has
// none, a token given is kept, and a token is removed when access is not by token.
func setGitAccess(st *gitApp, access, token string) error {
	st.Access = access

	if access == git.AccessKey && !pathExists(gitAppFile(st.App, ".key")) {
		if _, err := generateGitKey(st.App); err != nil {
			return err
		}
	}

	if access == git.AccessToken && token != "" {
		return writeGitFile(gitAppFile(st.App, ".token"), []byte(token))
	}

	if access != git.AccessToken {
		if err := os.Remove(gitAppFile(st.App, ".token")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// CreateGitApp registers an app to deploy from a repository. Nothing is cloned: a check
// does that, once the owner has given the repository the deploy key.
func CreateGitApp(ctx context.Context, registration GitAppRegistration) (*GitAppView, error) {
	name := registration.Name
	if name == "" || name != loader.NormalizeProjectName(name) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not an app name: lowercase letters, digits, - and _, starting with a letter or a digit", name))
	}
	if !validGitBranch(registration.Branch) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", registration.Branch))
	}
	if err := checkGitAccess(registration.URL, registration.Access, registration.Token != ""); err != nil {
		return nil, err
	}

	end, err := Begin(name, "registration")
	if err != nil {
		return nil, err
	}
	defer end()

	projects, err := gitDocker.Projects(ctx)
	if err != nil {
		return nil, err
	}
	if _, taken := projects[name]; taken || pathExists(gitAppFile(name, ".json")) {
		return nil, ErrGitAppNameTaken
	}

	st := &gitApp{
		App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
		Remote: registration.URL, Branch: registration.Branch,
		History: []gitHistoryEntry{}, Attempted: []string{},
	}
	if err := setGitAccess(st, registration.Access, registration.Token); err != nil {
		return nil, err
	}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// findGitApp is a registered app, or an app to adopt: a compose project whose folder is
// the top of a git work tree, described from that folder and not saved.
func findGitApp(ctx context.Context, name string) (*gitApp, error) {
	st, err := loadGitApp(name)
	if !errors.Is(err, ErrGitAppNotFound) {
		return st, err
	}

	projects, err := gitDocker.Projects(ctx)
	if err != nil {
		return nil, err
	}

	// the top only: a compose file below the root of a repository is not one this reads
	dir := projects[name]
	if dir == "" || !pathExists(filepath.Join(dir, ".git")) {
		return nil, ErrGitAppNotFound
	}

	info, err := git.Describe(ctx, dir)
	if err != nil {
		return nil, err
	}

	return &gitApp{
		App: name, Origin: gitOriginAdoptable, Dir: dir, Remote: info.RemoteURL, Branch: info.Branch,
		Access: git.AccessNone, Cloned: true, History: []gitHistoryEntry{}, Attempted: []string{},
	}, nil
}

// GetGitApp is the app as the API answers it.
func GetGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// adoptGitApp makes an adoptable app a git app. Its commit becomes the deployed version,
// and the images its containers run are kept under that commit's tags, so that the first
// deployment CasaOS makes has a version to roll back to.
func adoptGitApp(ctx context.Context, st *gitApp) error {
	switch {
	case st.Branch == "":
		return GitRequestError(fmt.Sprintf("%s is in detached HEAD: check a branch out to adopt it", st.Dir))
	case st.Remote == "":
		return GitRequestError(fmt.Sprintf("%s has no remote to follow", st.Dir))
	case gitURLKind(st.Remote) == "":
		return GitRequestError(fmt.Sprintf("the remote of %s, `%s`, is not a URL CasaOS can fetch: HTTPS, SSH or file, without a password in it", st.Dir, redactGitURL(st.Remote)))
	}

	info, err := git.Describe(ctx, st.Dir)
	if err != nil {
		return err
	}
	if info.Head == "" {
		return GitRequestError(fmt.Sprintf("%s has no commit yet", st.Dir))
	}

	images, err := gitDocker.TagRunning(ctx, st.App, st.Dir, info.Head)
	if err != nil {
		return err
	}

	subject, _ := git.Subject(ctx, st.Dir, info.Head)
	now := time.Now().UTC()
	st.Origin = gitOriginAdopted
	st.Deployed = &gitDeployment{Commit: info.Head, Subject: subject, At: now, Images: images}
	st.record(gitHistoryEntry{Commit: info.Head, Subject: subject, At: now, Outcome: gitOutcomeAdopted, Images: images})
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")

	return saveGitApp(st)
}

// UpdateGitApp changes the branch, the automatic rebuild or the access of a git app, and
// adopts an adoptable one.
func UpdateGitApp(ctx context.Context, name string, changes GitAppChanges) (*GitAppView, error) {
	end, err := Begin(name, "settings change")
	if err != nil {
		return nil, err
	}
	defer end()

	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}

	access, token := st.Access, ""
	if changes.Access != nil {
		access = *changes.Access
	}
	if changes.Token != nil {
		if *changes.Token == "" {
			return nil, GitRequestError("a token cannot be empty: switch access to none or key to forget it")
		}
		token = *changes.Token
	}
	accessChanged := changes.Access != nil || changes.Token != nil
	if accessChanged {
		if err := checkGitAccess(st.Remote, access, token != "" || pathExists(gitAppFile(name, ".token"))); err != nil {
			return nil, err
		}
	}

	if changes.Branch != nil && *changes.Branch != st.Branch {
		if st.Cloned {
			return nil, GitRequestError(fmt.Sprintf("the branch of %s is the one its folder is on: check another branch out there instead", name))
		}
		if !validGitBranch(*changes.Branch) {
			return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", *changes.Branch))
		}
		st.Branch = *changes.Branch
	}

	if st.Origin == gitOriginAdoptable {
		if err := adoptGitApp(ctx, st); err != nil {
			return nil, err
		}
	}

	if changes.AutoDeploy != nil {
		st.AutoDeploy = *changes.AutoDeploy
		if st.AutoDeploy {
			st.AutoPaused = false
		}
	}
	if accessChanged {
		if err := setGitAccess(st, access, token); err != nil {
			return nil, err
		}
	}

	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// DeleteGitApp removes a registered app no deployment has succeeded for: what a failed
// first deployment left (its containers, networks and images), the clone of a created app,
// its state and its secrets.
func DeleteGitApp(ctx context.Context, name string) error {
	end, err := Begin(name, "removal")
	if err != nil {
		return err
	}
	defer end()

	st, err := loadGitApp(name)
	if err != nil {
		return err
	}
	if st.Deployed != nil {
		return ErrGitAppDeployed
	}

	if err := gitDocker.Remove(ctx, name); err != nil {
		return err
	}
	if images := gitImagesOf(st); len(images) > 0 {
		gitDocker.RemoveImages(ctx, images)
	}

	if st.Origin == gitOriginCreated && st.Cloned {
		if err := os.RemoveAll(st.Dir); err != nil {
			return err
		}
	}

	return forgetGitApp(name)
}

// forgetUninstalledGitApp removes what CasaOS kept of a git app that was uninstalled: the
// git- tags of its versions, its state and its secrets.
func forgetUninstalledGitApp(ctx context.Context, st *gitApp) {
	if images := gitImagesOf(st); len(images) > 0 {
		gitDocker.RemoveImages(ctx, images)
	}

	if err := forgetGitApp(st.App); err != nil {
		logger.Error("the state of an uninstalled git app could not be removed", zap.Error(err), zap.String("app", st.App))
	}
}

// gitImagesOf is every git- tag the app's remembered versions name, sorted.
func gitImagesOf(st *gitApp) []string {
	images := []string{}
	if st.Deployed != nil {
		images = append(images, lo.Values(st.Deployed.Images)...)
	}
	for _, entry := range st.History {
		images = append(images, lo.Values(entry.Images)...)
	}
	images = lo.Uniq(images)
	sort.Strings(images)

	return images
}

// CheckGitApp is "Check now". An adoptable app is adopted here, so that a folder that
// cannot be adopted is refused with its reason; the check itself runs in the background,
// then whatever the automatic rebuild may deploy, and the app is returned as the check
// starts.
func CheckGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, end, err := beginGitCheck(ctx, name)
	if err != nil {
		return nil, err
	}

	// read before the check runs: it changes st from here on
	view := newGitAppView(ctx, st)

	go func() {
		ctx := context.Background()
		checkGitApp(ctx, st, true)
		deployGitAppAutomatically(ctx, name, end)
	}()

	return view, nil
}

// beginGitCheck claims the app for a check, adopts it when it is adoptable, and records
// the operation. The caller releases end once the check and what it starts are over.
func beginGitCheck(ctx context.Context, name string) (*gitApp, func(), error) {
	end, err := Begin(name, gitOperationCheck)
	if err != nil {
		return nil, nil, err
	}

	st, err := findGitApp(ctx, name)
	if err == nil && st.Origin == gitOriginAdoptable {
		err = adoptGitApp(ctx, st)
	}
	if err == nil {
		st.Operation = &gitOperation{Kind: gitOperationCheck, StartedAt: time.Now().UTC()}
		err = saveGitApp(st)
	}
	if err != nil {
		end()
		return nil, nil, err
	}

	return st, end, nil
}

// checkGitApp asks the remote where the branch is and records the answer, then clears the
// operation. With clone, a registered app that is not cloned yet is cloned and its compose
// files validated. Whatever fails is recorded in the check, and changes nothing else.
func checkGitApp(ctx context.Context, st *gitApp, clone bool) {
	defer func() {
		st.Operation = nil
		if err := saveGitApp(st); err != nil {
			logger.Error("the check of a git app could not be written down", zap.Error(err), zap.String("app", st.App))
		}
	}()

	previous := ""
	if st.Check != nil {
		previous = st.Check.RemoteCommit
	}
	st.Check = &gitCheck{At: time.Now().UTC(), RemoteCommit: previous}
	st.NoComposeFile = false

	auth, err := gitAuth(st)
	if err == nil && st.Branch == "" {
		st.Branch, err = git.DefaultBranch(ctx, st.Remote, auth)
	}
	commit := ""
	if err == nil {
		commit, err = git.LsRemote(ctx, st.Remote, st.Branch, auth)
	}
	if err != nil {
		st.Check.Error = err.Error()
		return
	}
	st.Check.RemoteCommit = commit

	if st.Cloned || !clone {
		return
	}

	if err := cloneGitApp(ctx, st, auth); err != nil {
		st.Check.Error = err.Error()
	}
}

// cloneGitApp clones a created app into its folder, which must be absent or empty, and
// keeps the clone only when it holds a compose file.
func cloneGitApp(ctx context.Context, st *gitApp, auth git.Auth) error {
	if entries, err := os.ReadDir(st.Dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s already exists and is not empty", st.Dir)
	}

	// beside the folder, then moved in whole: a clone cut short leaves nothing where the
	// app goes, and what is left beside it is CasaOS's own to remove
	partial := gitPartialClone(st)
	_ = os.RemoveAll(partial)
	if err := git.Clone(ctx, st.Remote, st.Branch, partial, auth); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}

	if _, err := gitComposeSummary(partial); err != nil {
		_ = os.RemoveAll(partial)
		st.NoComposeFile = errors.Is(err, ErrGitNoComposeFile)

		return err
	}

	_ = os.Remove(st.Dir) // an empty folder in the way
	if err := os.Rename(partial, st.Dir); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}

	st.Cloned = true
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")

	return nil
}

// gitPartialClone is where a created app is cloned before it is moved into its folder.
func gitPartialClone(st *gitApp) string {
	return filepath.Join(filepath.Dir(st.Dir), "."+st.App+".clone")
}
