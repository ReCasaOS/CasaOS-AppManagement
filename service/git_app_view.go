package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

// A git app as the API answers it: what CasaOS keeps, and what the folder says now.

// GitAppView is the `GitApp` of the API.
type GitAppView struct {
	App        string           `json:"app"`
	Origin     string           `json:"origin"`
	Dir        string           `json:"dir"`
	Remote     string           `json:"remote"`
	Branch     string           `json:"branch"`
	Access     string           `json:"access"`
	PublicKey  string           `json:"public_key,omitempty"`
	TokenSet   bool             `json:"token_set"`
	AutoDeploy bool             `json:"auto_deploy"`
	AutoPaused bool             `json:"auto_paused"`
	Blocked    bool             `json:"blocked"`
	EnvTracked bool             `json:"env_tracked"`
	Cloned     bool             `json:"cloned"`
	Head       *GitHeadView     `json:"head"`
	Deployed   *GitDeployedView `json:"deployed"`
	Check      *GitCheckView    `json:"check"`
	NewCommits bool             `json:"new_commits"`
	State      string           `json:"state"`
	Operation  *gitOperation    `json:"operation"`
	History    []GitHistoryView `json:"history"`
	Compose    *GitComposeView  `json:"compose"`
	// ComposeExample is set when the last check found no compose file in the repository.
	ComposeExample *string        `json:"compose_example"`
	EnvTemplate    *string        `json:"env_template"`
	BuildLog       string         `json:"build_log"`
	Webhook        GitWebhookView `json:"webhook"`
	// Follow is `branch` or `tags`. TagPattern and Prereleases choose the tags an app that
	// follows them may deploy, and are kept while it follows a branch.
	Follow      string `json:"follow"`
	TagPattern  string `json:"tag_pattern"`
	Prereleases bool   `json:"prereleases"`
}

type GitHeadView struct {
	Commit            string `json:"commit"`
	Subject           string `json:"subject"`
	TrackedFilesClean bool   `json:"tracked_files_clean"`
}

type GitDeployedView struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Tag     string    `json:"tag"`
}

// GitCheckView is the `check` of the API: the last check, and whether it found the deployed
// tag on another commit.
type GitCheckView struct {
	At           time.Time `json:"at"`
	RemoteCommit string    `json:"remote_commit"`
	RemoteTag    string    `json:"remote_tag"`
	TagMoved     bool      `json:"tag_moved"`
	Error        string    `json:"error"`
}

type GitHistoryView struct {
	Commit     string    `json:"commit"`
	Subject    string    `json:"subject"`
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason"`
	Revertable bool      `json:"revertable"`
	Tag        string    `json:"tag"`
}

type GitComposeView struct {
	Files    []string                `json:"files"`
	Services []GitComposeServiceView `json:"services"`
}

type GitComposeServiceView struct {
	Name      string   `json:"name"`
	Build     bool     `json:"build"`
	Image     string   `json:"image"`
	Ports     []string `json:"ports"`
	Volumes   []string `json:"volumes"`
	Sensitive []string `json:"sensitive"`
}

// GitComposeExample is what a repository without a compose file is told to add.
const GitComposeExample = `services:
  app:
    build: .
    ports:
      - "8080:8080"
    restart: unless-stopped
`

// gitBuildLogTail is how much of the last build's log the API returns.
const gitBuildLogTail = 64 << 10

// gitAppStateOf is the one state the card and the tab show.
func gitAppStateOf(st *gitApp) string {
	if st.Operation != nil {
		switch st.Operation.Kind {
		case gitOperationBuild:
			return "building"
		case gitOperationDeploy, gitOperationRevert:
			return "deploying"
		}
	}

	newest := ""
	if len(st.History) > 0 {
		newest = st.History[0].Outcome
	}

	switch {
	case st.Blocked || newest == gitOutcomeFailed:
		return "failed"
	case newest == gitOutcomeBuildFailed, newest == gitOutcomeRolledBack, newest == gitOutcomeInterrupted:
		return newest
	case st.Check != nil && st.Check.Error != "":
		return "unreachable"
	}

	return "idle"
}

// gitNewCommits reports whether the last check saw a version to deploy: the branch somewhere
// else than what runs, or for an app that follows tags a tag higher than the one deployed (any
// other tag, before its first deployment in that mode). A check that found no tag, such as the
// branch check left from before a switch to tags, sees nothing to deploy. Nothing runs before
// the first deployment, so nothing is new either.
func gitNewCommits(st *gitApp) bool {
	if st.Deployed == nil || st.Check == nil || st.Check.RemoteCommit == "" || st.Check.RemoteCommit == st.Deployed.Commit {
		return false
	}
	if !st.followsTags() {
		return true
	}

	return st.Check.RemoteTag != "" && (st.Deployed.Tag == "" || gitTagHigher(st.Check.RemoteTag, st.Deployed.Tag))
}

// GitAppGrid is what the app card shows of a registered git app, nil for any other app.
// It reads the state file alone: the grid never waits on git or on a registry.
func GitAppGrid(app string) *codegen.GitAppGrid {
	st, err := loadGitApp(app)
	if err != nil {
		return nil
	}

	return &codegen.GitAppGrid{NewCommits: gitNewCommits(st), State: gitAppStateOf(st), Deployed: st.Deployed != nil}
}

// GitAppsWithoutContainers are the grid items of the registered git apps missing from the
// compose list, which only knows apps that have containers: never deployed, or left
// without one by a failed first deployment. The owner still reaches their Repository tab.
func GitAppsWithoutContainers(listed []string) []codegen.WebAppGridItem {
	names, err := GitAppNames()
	if err != nil {
		return nil
	}

	items := []codegen.WebAppGridItem{}
	for _, name := range names {
		if lo.Contains(listed, name) {
			continue
		}

		if grid := GitAppGrid(name); grid != nil {
			items = append(items, codegen.WebAppGridItem{
				AppType: codegen.V2app,
				Name:    lo.ToPtr(name),
				Title:   &map[string]string{common.DefaultLanguage: name},
				Status:  lo.ToPtr(""),
				Git:     grid,
			})
		}
	}

	return items
}

// newGitAppView reads what the API answers for st.
//
// ponytail: a view of a cloned app runs git four times; cache it per HEAD if polling it
// ever shows up on a small box.
func newGitAppView(ctx context.Context, st *gitApp) *GitAppView {
	view := &GitAppView{
		App: st.App, Origin: st.Origin, Dir: st.Dir, Remote: redactGitURL(st.Remote), Branch: st.Branch, Access: st.Access,
		TokenSet:   pathExists(gitAppFile(st.App, ".token")),
		AutoDeploy: st.AutoDeploy, AutoPaused: st.AutoPaused, Blocked: st.Blocked,
		EnvTracked: st.EnvTracked, Cloned: st.Cloned,
		NewCommits: gitNewCommits(st), State: gitAppStateOf(st),
		History:     []GitHistoryView{},
		BuildLog:    readGitBuildLogTail(st.App),
		Webhook:     gitWebhookView(st.App),
		Follow:      st.follow(),
		TagPattern:  st.TagPattern,
		Prereleases: st.Prereleases,
	}

	// copies: a deployment goes on changing st once its view is taken
	if st.Check != nil {
		view.Check = &GitCheckView{
			At: st.Check.At, RemoteCommit: st.Check.RemoteCommit, RemoteTag: st.Check.RemoteTag, TagMoved: gitTagMoved(st),
			Error: st.Check.Error,
		}
	}
	if st.Operation != nil {
		operation := *st.Operation
		view.Operation = &operation
	}

	if st.NoComposeFile {
		view.ComposeExample = lo.ToPtr(GitComposeExample)
	}

	if st.Access == git.AccessKey {
		if public, err := os.ReadFile(gitAppFile(st.App, ".key.pub")); err == nil {
			view.PublicKey = strings.TrimSpace(string(public))
		}
	}

	if st.Deployed != nil {
		view.Deployed = &GitDeployedView{Commit: st.Deployed.Commit, Subject: st.Deployed.Subject, At: st.Deployed.At, Tag: st.Deployed.Tag}
	}

	for _, entry := range st.History {
		view.History = append(view.History, GitHistoryView{
			Commit: entry.Commit, Tag: entry.Tag, Subject: entry.Subject, At: entry.At, Outcome: entry.Outcome, Reason: entry.Reason,
			Revertable: gitRevertable(ctx, st, entry),
		})
	}

	if !st.Cloned {
		return view
	}

	if info, err := git.Describe(ctx, st.Dir); err == nil && info.Head != "" {
		subject, _ := git.Subject(ctx, st.Dir, info.Head)
		view.Head = &GitHeadView{Commit: info.Head, Subject: subject, TrackedFilesClean: info.TrackedFilesClean}
	}
	if tracked, err := git.IsTracked(ctx, st.Dir, ".env"); err == nil {
		view.EnvTracked = tracked
	}
	view.Compose, _ = gitComposeSummary(st.Dir)
	view.EnvTemplate = gitEnvTemplate(st.Dir)

	return view
}

// gitRevertable reports whether the history can go back to entry: a version that ran,
// that is not the one running, and whose images are still there.
func gitRevertable(ctx context.Context, st *gitApp, entry gitHistoryEntry) bool {
	if entry.Outcome != gitOutcomeDeployed && entry.Outcome != gitOutcomeAdopted {
		return false
	}
	if st.Deployed != nil && entry.Commit == st.Deployed.Commit {
		return false
	}

	return gitDocker.ImagesExist(ctx, entry.Images)
}

func readGitBuildLogTail(app string) string {
	file, err := os.Open(gitAppFile(app, ".build.log"))
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}

	offset := max(info.Size()-gitBuildLogTail, 0)
	buf := make([]byte, info.Size()-offset)
	n, _ := file.ReadAt(buf, offset)

	return string(buf[:n])
}

func gitEnvTemplate(root string) *string {
	for _, name := range []string{".env.example", ".env.sample"} {
		if buf, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			text := string(buf)
			return &text
		}
	}

	return nil
}

// gitComposeService is what the review shows of a service, read from the file as written:
// before its .env exists a reference cannot be resolved, and what the owner reviews is
// the file.
type gitComposeService struct {
	Image       string `yaml:"image"`
	Build       any    `yaml:"build"`
	Ports       []any  `yaml:"ports"`
	Volumes     []any  `yaml:"volumes"`
	Privileged  any    `yaml:"privileged"`
	NetworkMode string `yaml:"network_mode"`
	Pid         string `yaml:"pid"`
	CapAdd      []any  `yaml:"cap_add"`
	Devices     []any  `yaml:"devices"`
}

// gitComposeSummary is what the compose files at root will run.
//
// ponytail: `extends` and `include` are not followed; follow them when a repository
// relies on them for what it runs.
func gitComposeSummary(root string) (*GitComposeView, error) {
	files, err := gitComposeFiles(root)
	if err != nil {
		return nil, err
	}

	merged := map[string]*gitComposeService{}
	view := &GitComposeView{Files: []string{}, Services: []GitComposeServiceView{}}
	for _, file := range files {
		view.Files = append(view.Files, filepath.Base(file))

		buf, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}

		var parsed struct {
			Services map[string]gitComposeService `yaml:"services"`
		}
		if err := yaml.Unmarshal(buf, &parsed); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
		}

		for name, service := range parsed.Services {
			if merged[name] == nil {
				service := service
				merged[name] = &service
				continue
			}
			merged[name].override(service)
		}
	}

	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		view.Services = append(view.Services, merged[name].view(name))
	}

	return view, nil
}

// override applies an override file's service, the way compose merges these fields: what
// is set replaces, lists add up.
func (s *gitComposeService) override(o gitComposeService) {
	if o.Image != "" {
		s.Image = o.Image
	}
	if o.Build != nil {
		s.Build = o.Build
	}
	if o.Privileged != nil {
		s.Privileged = o.Privileged
	}
	if o.NetworkMode != "" {
		s.NetworkMode = o.NetworkMode
	}
	if o.Pid != "" {
		s.Pid = o.Pid
	}
	s.Ports = append(s.Ports, o.Ports...)
	s.Volumes = append(s.Volumes, o.Volumes...)
	s.CapAdd = append(s.CapAdd, o.CapAdd...)
	s.Devices = append(s.Devices, o.Devices...)
}

func (s *gitComposeService) view(name string) GitComposeServiceView {
	view := GitComposeServiceView{
		Name: name, Build: s.Build != nil, Image: s.Image,
		Ports: []string{}, Volumes: []string{}, Sensitive: []string{},
	}

	for _, port := range s.Ports {
		view.Ports = append(view.Ports, composeEntry(port, "published"))
	}

	socket := false
	for _, volume := range s.Volumes {
		entry := composeEntry(volume, "source")
		view.Volumes = append(view.Volumes, entry)

		source, _, _ := strings.Cut(entry, ":")
		socket = socket || source == "/var/run/docker.sock" || source == "/run/docker.sock"
	}

	// anything but an explicit false: a reference may well resolve to true
	if s.Privileged != nil && s.Privileged != false && s.Privileged != "false" {
		view.Sensitive = append(view.Sensitive, "privileged")
	}
	if s.NetworkMode == "host" {
		view.Sensitive = append(view.Sensitive, "network_mode_host")
	}
	if s.Pid == "host" {
		view.Sensitive = append(view.Sensitive, "pid_host")
	}
	if len(s.CapAdd) > 0 {
		view.Sensitive = append(view.Sensitive, "cap_add")
	}
	if len(s.Devices) > 0 {
		view.Sensitive = append(view.Sensitive, "devices")
	}
	if socket {
		view.Sensitive = append(view.Sensitive, "docker_socket")
	}

	return view
}

// composeEntry is a port or a volume as written: the short syntax itself, the long one
// folded into it.
func composeEntry(entry any, source string) string {
	long, ok := entry.(map[string]any)
	if !ok {
		return fmt.Sprint(entry)
	}

	target := fmt.Sprint(long["target"])
	if value, ok := long[source]; ok && fmt.Sprint(value) != "" {
		return fmt.Sprint(value) + ":" + target
	}

	return target
}
