package service

import (
	"crypto/ed25519"
	"crypto/rand"
	stdjson "encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/samber/lo"
	"golang.org/x/crypto/ssh"
)

// What CasaOS remembers of an app deployed from its git repository: one JSON file per
// app, and its secrets beside it, in a folder only root reads.

var (
	// gitAppsDir holds the state and the secrets. Vars, so tests can point them elsewhere.
	gitAppsDir = "/var/lib/casaos/git_apps"
	// gitAppsDataRoot is where a created app is cloned.
	gitAppsDataRoot = "/DATA/AppData"
)

const (
	gitHistoryLength   = 3
	gitAttemptedLength = 20
)

// Origins of a git app.
const (
	gitOriginCreated   = "created"
	gitOriginAdopted   = "adopted"
	gitOriginAdoptable = "adoptable"
)

// Outcomes of a deployment.
const (
	gitOutcomeAdopted     = "adopted"
	gitOutcomeDeployed    = "deployed"
	gitOutcomeBuildFailed = "build_failed"
	gitOutcomeRolledBack  = "rolled_back"
	gitOutcomeFailed      = "failed"
	gitOutcomeInterrupted = "interrupted"
)

// Kinds of operation recorded while one runs.
const (
	gitOperationCheck  = "check"
	gitOperationBuild  = "build"
	gitOperationDeploy = "deploy"
	gitOperationRevert = "revert"
)

// ErrGitAppNotFound is an app that is neither registered nor adoptable.
var ErrGitAppNotFound = errors.New("not an app deployed from a git repository")

type gitApp struct {
	App        string `json:"app"`
	Origin     string `json:"origin"`
	Dir        string `json:"dir"`
	Remote     string `json:"remote"`
	Branch     string `json:"branch"`
	Access     string `json:"access"`
	AutoDeploy bool   `json:"auto_deploy"`
	AutoPaused bool   `json:"auto_paused"`
	Blocked    bool   `json:"blocked"`
	EnvTracked bool   `json:"env_tracked"`
	// Cloned is set once a created app's clone holds a compose file; an adopted app is.
	Cloned bool `json:"cloned"`
	// NoComposeFile is set when the last check cloned a repository without a compose file.
	NoComposeFile bool              `json:"no_compose_file"`
	Deployed      *gitDeployment    `json:"deployed"`
	Check         *gitCheck         `json:"check"`
	Operation     *gitOperation     `json:"operation"`
	History       []gitHistoryEntry `json:"history"`
	// Attempted is what the automatic rebuild does not try again, the last 20.
	Attempted []string `json:"attempted"`
}

type gitDeployment struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	// Images are the git- tags of the running version, per service.
	Images map[string]string `json:"images"`
}

type gitCheck struct {
	At           time.Time `json:"at"`
	RemoteCommit string    `json:"remote_commit"`
	Error        string    `json:"error"`
}

type gitOperation struct {
	Kind      string    `json:"kind"`
	Commit    string    `json:"commit"`
	StartedAt time.Time `json:"started_at"`
}

type gitHistoryEntry struct {
	Commit  string            `json:"commit"`
	Subject string            `json:"subject"`
	At      time.Time         `json:"at"`
	Outcome string            `json:"outcome"`
	Reason  string            `json:"reason"`
	Images  map[string]string `json:"images"`
}

var scpLikeGitURL = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9.-]+:\S+$`)

// gitTransportHelper is git's `<transport>::<address>`: whatever follows, git hands the
// address to a remote helper (ext runs a command, fd reads a file descriptor).
var gitTransportHelper = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

// gitURLKind is `http`, `ssh` or `file` for a URL git may be given, and empty for anything
// else: another transport, an option in disguise, or a password written in the URL, which
// belongs in a token.
func gitURLKind(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n") || gitTransportHelper.MatchString(raw) {
		return ""
	}

	if !strings.Contains(raw, "://") {
		if scpLikeGitURL.MatchString(raw) {
			return "ssh"
		}

		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if _, password := u.User.Password(); password {
		return ""
	}

	switch {
	case (u.Scheme == "https" || u.Scheme == "http") && u.Host != "":
		return "http"
	case u.Scheme == "ssh" && u.Host != "":
		return "ssh"
	case u.Scheme == "file" && u.Path != "":
		return "file"
	}

	return ""
}

// redactGitURL is a URL fit to show: a password written in it is dropped.
func redactGitURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, password := u.User.Password(); !password {
		return raw
	}
	u.User = url.User(u.User.Username())

	return u.String()
}

// validGitBranch is a name git reads as a branch name: not an option, not a revision
// expression (`..`, `@`, `@{`, `~`, `^`), and no character a ref name forbids.
func validGitBranch(branch string) bool {
	return !strings.HasPrefix(branch, "-") && branch != "@" && !strings.Contains(branch, "..") && !strings.Contains(branch, "@{") &&
		!strings.ContainsAny(branch, " ~^:?*[\\") && !strings.ContainsFunc(branch, unicode.IsControl)
}

// gitAppFile is one of an app's files: .json, .key, .key.pub, .token, .known_hosts,
// .build.log or .webhook.
func gitAppFile(app, suffix string) string {
	return filepath.Join(gitAppsDir, app+suffix)
}

func loadGitApp(app string) (*gitApp, error) {
	// a name from a request: anything but a compose project name is no app, and never a path
	if app == "" || app != loader.NormalizeProjectName(app) {
		return nil, ErrGitAppNotFound
	}

	buf, err := os.ReadFile(gitAppFile(app, ".json"))
	if os.IsNotExist(err) {
		return nil, ErrGitAppNotFound
	}
	if err != nil {
		return nil, err
	}

	var st gitApp
	if err := stdjson.Unmarshal(buf, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", gitAppFile(app, ".json"), err)
	}

	return &st, nil
}

func saveGitApp(st *gitApp) error {
	buf, err := stdjson.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}

	return writeGitFile(gitAppFile(st.App, ".json"), buf)
}

// GitAppNames is every registered git app, sorted.
func GitAppNames() ([]string, error) {
	entries, err := os.ReadDir(gitAppsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	names := []string{}
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok && !entry.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	return names, nil
}

// forgetGitApp removes an app's state and secrets.
func forgetGitApp(app string) error {
	// a delivery recorded meanwhile would write the webhook's secret back
	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	failures := []error{}
	for _, suffix := range []string{".webhook", ".json", ".key", ".key.pub", ".token", ".known_hosts", ".build.log"} {
		if err := os.Remove(gitAppFile(app, suffix)); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}

// writeGitFile replaces a file of the git apps' folder the way saveImageUpdates replaces
// its own: a temporary file of its own name, a sync, a rename. The next start reads these
// files, and a truncated state is an app CasaOS no longer knows how to deploy.
func writeGitFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}

	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}

	return err
}

// generateGitKey writes a new ed25519 deploy key for app and returns its public half, the
// line the owner adds to the repository.
func generateGitKey(app string) (string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}

	block, err := ssh.MarshalPrivateKey(private, "casaos-"+app)
	if err != nil {
		return "", err
	}

	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return "", err
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic))) + " casaos-" + app

	if err := writeGitFile(gitAppFile(app, ".key"), pem.EncodeToMemory(block)); err != nil {
		return "", err
	}
	if err := writeGitFile(gitAppFile(app, ".key.pub"), []byte(authorized+"\n")); err != nil {
		return "", err
	}
	if !pathExists(gitAppFile(app, ".known_hosts")) {
		if err := writeGitFile(gitAppFile(app, ".known_hosts"), nil); err != nil {
			return "", err
		}
	}

	return authorized, nil
}

// gitAuth is how the app's remote is reached.
func gitAuth(st *gitApp) (git.Auth, error) {
	switch st.Access {
	case git.AccessKey:
		return git.Auth{Mode: git.AccessKey, KeyPath: gitAppFile(st.App, ".key"), KnownHostsPath: gitAppFile(st.App, ".known_hosts")}, nil
	case git.AccessToken:
		token, err := os.ReadFile(gitAppFile(st.App, ".token"))
		if err != nil {
			return git.Auth{}, fmt.Errorf("the token of %s cannot be read: %w", st.App, err)
		}

		// the askpass helper goes beside the state, not under /tmp, which may be mounted noexec
		return git.Auth{Mode: git.AccessToken, Token: strings.TrimSpace(string(token)), AskpassDir: gitAppsDir}, nil
	}

	return git.Auth{Mode: git.AccessNone}, nil
}

// record puts entry at the top of the history and keeps the last three. It returns the
// images of the entries that left, except those a remaining entry or the running version
// still names: they are what can be removed.
func (st *gitApp) record(entry gitHistoryEntry) []string {
	st.History = append([]gitHistoryEntry{entry}, st.History...)
	if len(st.History) <= gitHistoryLength {
		return nil
	}

	gone := st.History[gitHistoryLength:]
	st.History = st.History[:gitHistoryLength]

	kept := map[string]bool{}
	for _, e := range st.History {
		for _, image := range e.Images {
			kept[image] = true
		}
	}
	if st.Deployed != nil {
		for _, image := range st.Deployed.Images {
			kept[image] = true
		}
	}

	removable := []string{}
	for _, e := range gone {
		for _, image := range lo.Values(e.Images) {
			if !kept[image] {
				removable = append(removable, image)
				kept[image] = true
			}
		}
	}
	sort.Strings(removable)

	return removable
}

// attempt remembers that commit was tried, so the automatic rebuild leaves it alone.
func (st *gitApp) attempt(commit string) {
	if commit == "" || lo.Contains(st.Attempted, commit) {
		return
	}

	st.Attempted = append(st.Attempted, commit)
	if len(st.Attempted) > gitAttemptedLength {
		st.Attempted = st.Attempted[len(st.Attempted)-gitAttemptedLength:]
	}
}

// historyEntry is the newest entry for commit of a version that ran, deployed or adopted, which
// is what a revert goes back to; failing that the newest entry for commit, or nil. A revert that
// failed leaves a newer entry for its commit than the one the history offers to revert to.
func (st *gitApp) historyEntry(commit string) *gitHistoryEntry {
	var newest *gitHistoryEntry
	for i := range st.History {
		entry := &st.History[i]
		if entry.Commit != commit {
			continue
		}
		if entry.Outcome == gitOutcomeDeployed || entry.Outcome == gitOutcomeAdopted {
			return entry
		}
		if newest == nil {
			newest = entry
		}
	}

	return newest
}
