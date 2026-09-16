package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestGitAppStateFollowsTheRule(t *testing.T) {
	failedCheck := &gitCheck{Error: "could not read from remote"}
	outcome := func(o string) []gitHistoryEntry { return []gitHistoryEntry{{Outcome: o}} }

	for want, st := range map[string]*gitApp{
		"building":    {Operation: &gitOperation{Kind: gitOperationBuild}, Blocked: true},
		"deploying":   {Operation: &gitOperation{Kind: gitOperationRevert}},
		"failed":      {Blocked: true, History: outcome(gitOutcomeDeployed)},
		"rolled_back": {History: outcome(gitOutcomeRolledBack), Check: failedCheck},
		"unreachable": {Operation: &gitOperation{Kind: gitOperationCheck}, History: outcome(gitOutcomeDeployed), Check: failedCheck},
		"idle":        {History: outcome(gitOutcomeAdopted), Check: &gitCheck{}},
	} {
		assert.Equal(t, gitAppStateOf(st), want)
	}

	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeFailed)}), "failed")
	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeBuildFailed)}), "build_failed")
	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeInterrupted)}), "interrupted")
	assert.Equal(t, gitAppStateOf(&gitApp{Operation: &gitOperation{Kind: gitOperationDeploy}}), "deploying")
}

func TestNewCommitsIsTheRemoteMovingAwayFromWhatRuns(t *testing.T) {
	deployed := &gitDeployment{Commit: "a"}

	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed}), "never checked")
	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{Error: "offline"}}))
	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "a"}}))
	assert.Assert(t, !gitNewCommits(&gitApp{Check: &gitCheck{RemoteCommit: "b"}}), "nothing runs yet")
	assert.Assert(t, gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "b"}}))
}

func TestTheComposeReviewShowsTheFilesAsWrittenAndWhatIsSensitive(t *testing.T) {
	root := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(`services:
  web:
    build: .
    ports:
      - "${PORT:-8080}:8080"
    volumes:
      - ./data:/data
      - /var/run/docker.sock:/var/run/docker.sock:ro
  agent:
    image: portainer/agent:2
    privileged: true
    network_mode: host
    pid: host
    cap_add: [NET_ADMIN]
    devices: [/dev/dri]
`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(root, "compose.override.yaml"), []byte(`services:
  web:
    ports:
      - target: 9090
        published: "9090"
`), 0o644))

	summary, err := gitComposeSummary(root)
	assert.NilError(t, err)
	assert.DeepEqual(t, summary, &GitComposeView{
		Files: []string{"compose.yaml", "compose.override.yaml"},
		Services: []GitComposeServiceView{
			{
				Name: "agent", Image: "portainer/agent:2", Ports: []string{}, Volumes: []string{},
				Sensitive: []string{"privileged", "network_mode_host", "pid_host", "cap_add", "devices"},
			},
			{
				Name: "web", Build: true, Ports: []string{"${PORT:-8080}:8080", "9090:9090"},
				Volumes:   []string{"./data:/data", "/var/run/docker.sock:/var/run/docker.sock:ro"},
				Sensitive: []string{"docker_socket"},
			},
		},
	})
}

// The view is the API's `GitApp`: the fields the dashboard reads, null where the contract
// says null, and the secrets never.
func TestTheViewOfAGitAppIsTheContract(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()

	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	second := pushTestCommit(t, work, ".env.example", "GREETING=hello\n")
	dir := filepath.Join(gitAppsDataRoot, "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".token"), []byte("secret")))
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".build.log"), []byte("#1 building\n")))

	at := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	st := &gitApp{
		App: "jarvis", Origin: gitOriginCreated, Dir: dir, Remote: url, Branch: "main", Access: git.AccessToken, Cloned: true,
		Deployed: &gitDeployment{Commit: second, Subject: "change .env.example", At: at, Images: map[string]string{"web": "jarvis-web:git-" + second[:12]}},
		Check:    &gitCheck{At: at, RemoteCommit: second},
		History: []gitHistoryEntry{
			{Commit: second, Subject: "change .env.example", At: at, Outcome: gitOutcomeDeployed, Images: map[string]string{"web": "jarvis-web:git-" + second[:12]}},
			{Commit: first, Subject: "change compose.yaml", At: at, Outcome: gitOutcomeDeployed, Images: map[string]string{"web": "jarvis-web:git-" + first[:12]}},
		},
	}

	encoded, err := stdjson.Marshal(newGitAppView(ctx, st))
	assert.NilError(t, err)
	var view map[string]any
	assert.NilError(t, stdjson.Unmarshal(encoded, &view))

	keys := []string{}
	for key := range view {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "head", "history", "new_commits", "operation",
		"origin", "remote", "state", "token_set",
	})
	assert.Equal(t, view["operation"], nil)
	assert.Equal(t, view["compose_example"], nil)
	assert.Equal(t, view["state"], "idle")
	assert.Equal(t, view["token_set"], true)
	assert.Equal(t, view["env_template"], "GREETING=hello\n")
	assert.Equal(t, view["build_log"], "#1 building\n")
	assert.DeepEqual(t, view["head"], map[string]any{"commit": second, "subject": "change .env.example", "tracked_files_clean": true})
	assert.DeepEqual(t, view["deployed"], map[string]any{"commit": second, "subject": "change .env.example", "at": "2026-09-16T10:00:00Z"})
	assert.DeepEqual(t, view["check"], map[string]any{"at": "2026-09-16T10:00:00Z", "remote_commit": second, "error": ""})

	history := view["history"].([]any)
	assert.Equal(t, history[0].(map[string]any)["revertable"], false, "the running version")
	assert.Equal(t, history[1].(map[string]any)["revertable"], true)

	fake.missing["jarvis-web:git-"+first[:12]] = true
	assert.Equal(t, newGitAppView(ctx, st).History[1].Revertable, false, "its image is gone")

	// a key is shown; an app that is not cloned has nothing of its folder
	public, err := generateGitKey("jarvis")
	assert.NilError(t, err)
	st.Access, st.Cloned, st.NoComposeFile = git.AccessKey, false, true
	notCloned := newGitAppView(ctx, st)
	assert.Equal(t, notCloned.PublicKey, public)
	assert.Assert(t, notCloned.Head == nil && notCloned.Compose == nil && notCloned.EnvTemplate == nil)
	assert.Equal(t, *notCloned.ComposeExample, GitComposeExample)
}

func TestTheGridCarriesGitOnlyForGitApps(t *testing.T) {
	gitAppsIn(t)

	assert.Assert(t, GitAppGrid("nextcloud") == nil)

	assert.NilError(t, saveGitApp(&gitApp{
		App: "jarvis", Deployed: &gitDeployment{Commit: "a"}, Check: &gitCheck{RemoteCommit: "b"},
		History: []gitHistoryEntry{{Commit: "b", Outcome: gitOutcomeRolledBack}},
	}))
	assert.DeepEqual(t, GitAppGrid("jarvis"), &codegen.GitAppGrid{NewCommits: true, State: "rolled_back", Deployed: true})
}

// The compose list knows apps by their containers: a git app that has none still reaches
// the grid, so that its owner can see why and delete it.
func TestAGitAppWithoutContainersIsStillInTheGrid(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, saveGitApp(&gitApp{App: "jarvis", History: []gitHistoryEntry{{Outcome: gitOutcomeFailed}}}))
	assert.NilError(t, saveGitApp(&gitApp{App: "running", Deployed: &gitDeployment{Commit: "a"}}))

	items := GitAppsWithoutContainers([]string{"running", "nextcloud"})

	assert.Equal(t, len(items), 1)
	assert.Equal(t, items[0].AppType, codegen.V2app)
	assert.Equal(t, *items[0].Name, "jarvis")
	assert.DeepEqual(t, *items[0].Title, map[string]string{"en_us": "jarvis"})
	assert.Equal(t, *items[0].Status, "")
	assert.DeepEqual(t, items[0].Git, &codegen.GitAppGrid{State: "failed", Deployed: false})
}
