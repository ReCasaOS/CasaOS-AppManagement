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

// daemonTestRemote skips a test without a daemon. Otherwise it is a bare repository and a work
// tree that pushes to it, and the name of an app no other run uses, whose containers, networks
// and git- images are removed when the test ends.
func daemonTestRemote(t *testing.T) (name, url, work string, dockerClient client.APIClient) {
	t.Helper()

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

	name = fmt.Sprintf("casaos-git-%d", time.Now().UnixNano())

	root := t.TempDir()
	gitShell(t, root, "git init -q --bare --initial-branch=main remote.git && git clone -q remote.git work 2>/dev/null")
	work = filepath.Join(root, "work")
	url = "file://" + filepath.Join(root, "remote.git")
	gitShell(t, work, "git symbolic-ref HEAD refs/heads/main")

	backend, dockerClient, err := apiService()
	assert.NilError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = backend.Down(cleanup, name, api.DownOptions{RemoveOrphans: true, Images: "all"})
		if st, err := loadGitApp(name); err == nil {
			gitDocker.RemoveImages(cleanup, gitImagesOf(st))
		}
	})

	return name, url, work, dockerClient
}

// A git app against the daemon: created from a local repository, deployed, a new commit
// deployed automatically, a commit whose container exits rolled back, then a revert.
func TestAGitAppAgainstTheDaemon(t *testing.T) {
	name, url, work, dockerClient := daemonTestRemote(t)
	ctx := context.Background()
	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCOPY version /version\nCMD [\"sh\", \"-c\", \"cat /version; exec sleep 3600\"]\n"), 0o644))
	pushTestCommit(t, work, "compose.yaml", "services:\n  web:\n    build: .\n")
	first := pushTestCommit(t, work, "version", "v1\n")

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

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
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

// A commit that renames a service leaves no container of the old name behind: the switch
// removes what the version it starts does not define.
func TestARenamedServiceLeavesNoContainerAgainstTheDaemon(t *testing.T) {
	name, url, work, dockerClient := daemonTestRemote(t)
	ctx := context.Background()
	// the name compose gave the first service's image, which no container of the project names
	// once the service is renamed, so that the cleanup's Down does not know it
	t.Cleanup(func() {
		_, _ = dockerClient.ImageRemove(context.Background(), name+"-web", client.ImageRemoveOptions{})
	})
	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCMD [\"sleep\", \"3600\"]\n"), 0o644))
	pushTestCommit(t, work, "compose.yaml", "services:\n  web:\n    build: .\n")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, name).Cloned, true)
	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	st := waitForGitApp(t, name)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed, st.History[0].Reason)

	renamed := pushTestCommit(t, work, "compose.yaml", "services:\n  app:\n    build: .\n")
	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, name)
	assert.Equal(t, st.Deployed.Commit, renamed, st.History[0].Reason)

	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+name),
	})
	assert.NilError(t, err)
	assert.Equal(t, len(containers.Items), 1)
	assert.Equal(t, containers.Items[0].Labels[api.ServiceLabel], "app")
}
