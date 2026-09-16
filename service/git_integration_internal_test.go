package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appdocker "github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"gotest.tools/v3/assert"
)

// A git app against the daemon: created from a local repository, deployed, a new commit
// deployed automatically, a commit whose container exits rolled back, then a revert.
func TestAGitAppAgainstTheDaemon(t *testing.T) {
	if os.Getenv("CASAOS_INTEGRATION") == "" {
		t.Skip("set CASAOS_INTEGRATION=1 to run against a Docker daemon")
	}
	if !appdocker.IsDaemonRunning() {
		t.Skip("Docker daemon is not running")
	}

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	settle := gitSettleDelay
	gitSettleDelay = 3 * time.Second
	t.Cleanup(func() { gitSettleDelay = settle })

	ctx := context.Background()
	name := fmt.Sprintf("casaos-git-%d", time.Now().UnixNano())

	root := t.TempDir()
	gitShell(t, root, "git init -q --bare --initial-branch=main remote.git && git clone -q remote.git work 2>/dev/null")
	work := filepath.Join(root, "work")
	url := "file://" + filepath.Join(root, "remote.git")
	gitShell(t, work, "git symbolic-ref HEAD refs/heads/main")
	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCOPY version /version\nCMD [\"sh\", \"-c\", \"cat /version; exec sleep 3600\"]\n"), 0o644))
	pushTestCommit(t, work, "compose.yaml", "services:\n  web:\n    build: .\n")
	first := pushTestCommit(t, work, "version", "v1\n")

	backend, dockerClient, err := apiService()
	assert.NilError(t, err)
	defer dockerClient.Close()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = backend.Down(cleanup, name, api.DownOptions{RemoveOrphans: true, Images: "all"})
		if st, err := loadGitApp(name); err == nil {
			gitDocker.RemoveImages(cleanup, gitImagesOf(st))
		}
	})

	// what the running container was created from, as the tag of a commit names it
	runs := func(commit string) {
		t.Helper()
		containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
			Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+name),
		})
		assert.NilError(t, err)
		assert.Equal(t, len(containers.Items), 1)
		image, err := dockerClient.ImageInspect(ctx, name+"-web:git-"+commit[:12])
		assert.NilError(t, err)
		assert.Equal(t, containers.Items[0].ImageID, image.ID, "the container runs %s", commit[:12])
	}

	_, err = CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, name).Cloned, true)

	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	st := waitForGitApp(t, name)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed, st.History[0].Reason)
	runs(first)

	on := true
	_, err = UpdateGitApp(ctx, name, GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "version", "v2\n")
	st = checkTestApp(t, name)
	assert.Equal(t, st.Deployed.Commit, second, st.History[0].Reason)
	runs(second)

	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCMD [\"sh\", \"-c\", \"exit 1\"]\n"), 0o644))
	broken := pushTestCommit(t, work, "version", "broken\n")
	st = checkTestApp(t, name)
	assert.Equal(t, st.History[0].Commit, broken)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack, st.History[0].Reason)
	assert.Equal(t, st.Deployed.Commit, second)
	runs(second)

	_, err = DeployGitApp(ctx, name, first, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, name)
	assert.Equal(t, st.Deployed.Commit, first, st.History[0].Reason)
	assert.Equal(t, st.AutoPaused, true)
	runs(first)
}
