package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
	"go.uber.org/zap"
)

// What a git app needs of Docker, behind one interface so that a deployment's decisions
// are tested without a daemon. composeGitRuntime is the real thing; the integration test
// drives it.

// ErrGitNoComposeFile is a repository `docker compose up` would refuse at its root.
var ErrGitNoComposeFile = errors.New("the repository has no compose.yaml, compose.yml, docker-compose.yaml or docker-compose.yml at its root")

// gitComposeFileNames are the files `docker compose up` looks for, in its order.
var gitComposeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// gitComposeFiles is what `docker compose up` reads at root: the first compose file found
// and its .override counterpart when there is one.
func gitComposeFiles(root string) ([]string, error) {
	for _, name := range gitComposeFileNames {
		main := filepath.Join(root, name)
		if _, err := os.Stat(main); err != nil {
			continue
		}

		files := []string{main}
		ext := filepath.Ext(name)
		if override := filepath.Join(root, strings.TrimSuffix(name, ext)+".override"+ext); pathExists(override) {
			files = append(files, override)
		}

		return files, nil
	}

	return nil, ErrGitNoComposeFile
}

// gitImageTag is where a deployment keeps the image of a service: the repository of the
// name compose uses, tagged git- and the commit's first 12 hex digits.
func gitImageTag(image, commit string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", err
	}

	return reference.FamiliarName(named) + ":git-" + commit[:12], nil
}

// builtServiceNames is every service with a build section, sorted.
func builtServiceNames(services types.Services) []string {
	names := []string{}
	for _, name := range sortedServiceNames(services) {
		if services[name].Build != nil {
			names = append(names, name)
		}
	}

	return names
}

// loadGitProject loads the compose files at root under the app's name, interpolated from
// envFile when it exists. The environment of the services is not resolved: a worktree
// has no untracked file, so an `env_file: .env` would fail to load there, and nothing a
// build or a tag needs comes from it.
func loadGitProject(ctx context.Context, app, root, envFile string) (*types.Project, error) {
	files, err := gitComposeFiles(root)
	if err != nil {
		return nil, err
	}

	env := []string{"AppID=" + app}
	for k, v := range baseInterpolationMap() {
		env = append(env, k+"="+v)
	}

	fns := []cli.ProjectOptionsFn{cli.WithWorkingDirectory(root), cli.WithOsEnv}
	if pathExists(envFile) {
		fns = append(fns, cli.WithEnvFiles(envFile), cli.WithDotEnv)
	}
	fns = append(fns, cli.WithEnv(env), cli.WithName(app), cli.WithoutEnvironmentResolution)

	options, err := cli.NewProjectOptions(files, fns...)
	if err != nil {
		return nil, err
	}

	return options.LoadProject(ctx)
}

// failedBuiltContainer is the stricter judgement a deployment gets: a container of a
// built service that exited with an error, is restarting or reports itself unhealthy
// fails it, even when the start's own wait only ran out of time.
func failedBuiltContainer(containers map[string][]api.ContainerSummary, built []string) error {
	for _, name := range built {
		for _, c := range containers[name] {
			switch {
			case c.State == "restarting":
				return fmt.Errorf("container %s of %s is restarting", c.Name, name)
			case c.State == "exited" && c.ExitCode != 0:
				return fmt.Errorf("container %s of %s exited with code %d", c.Name, name, c.ExitCode)
			case strings.EqualFold(string(c.Health), "unhealthy"):
				return fmt.Errorf("container %s of %s is unhealthy", c.Name, name)
			}
		}
	}

	return nil
}

type gitRuntime interface {
	// Projects is every compose project on this machine, loadable or not, with the folder
	// of its first compose file.
	Projects(ctx context.Context) (map[string]string, error)
	// Build builds the services with a build section of the project at root, each under
	// its git- tag for commit, and returns the tag of each service.
	Build(ctx context.Context, app, root, envFile, commit string, out io.Writer) (map[string]string, error)
	// Retag points the names compose uses for the built services of the project at dir at
	// the images tagged for commit.
	Retag(ctx context.Context, app, dir, commit string) error
	// TagRunning tags, for commit, the images the containers of the built services of the
	// project at dir were created from.
	TagRunning(ctx context.Context, app, dir, commit string) (map[string]string, error)
	// Start pulls what is not built, creates and starts the app from dir through the path
	// every app takes, and judges its built services.
	Start(ctx context.Context, app, dir string) error
	// Stop stops the app's containers.
	Stop(ctx context.Context, app string) error
	// Remove removes the app's containers and networks, and keeps its volumes.
	Remove(ctx context.Context, app string) error
	// ImagesExist reports whether every image is still there.
	ImagesExist(ctx context.Context, images map[string]string) bool
	// RemoveImages removes the tags no container uses.
	RemoveImages(ctx context.Context, images []string)
}

// gitDocker is the runtime in use; tests replace it.
var gitDocker gitRuntime = composeGitRuntime{}

// gitSettleDelay is how long a started app runs before its built services are judged: a
// container that crashes on start is `running` for a moment first. A var for the tests.
var gitSettleDelay = 10 * time.Second

type composeGitRuntime struct{}

func (composeGitRuntime) Projects(ctx context.Context) (map[string]string, error) {
	backend, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	stacks, err := backend.List(ctx, api.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	projects := map[string]string{}
	for _, stack := range stacks {
		projects[stack.ID] = ""
		if first, _, _ := strings.Cut(stack.ConfigFiles, ","); strings.TrimSpace(first) != "" {
			projects[stack.ID] = filepath.Dir(strings.TrimSpace(first))
		}
	}

	return projects, nil
}

func (composeGitRuntime) Build(ctx context.Context, app, root, envFile, commit string, out io.Writer) (map[string]string, error) {
	project, err := loadGitProject(ctx, app, root, envFile)
	if err != nil {
		return nil, err
	}

	images := map[string]string{}
	built := builtServiceNames(project.Services)
	for _, name := range built {
		service := project.Services[name]
		tag, err := gitImageTag(api.GetImageNameOrDefault(service, app), commit)
		if err != nil {
			return nil, err
		}

		// under this commit's tag, never under the name the running app uses: a build
		// that fails half-way leaves that name where it was
		service.Image = tag
		project.Services[name] = service
		images[name] = tag
	}
	if len(built) == 0 {
		return images, nil
	}

	// the build log goes where the caller says, not to AppManagement's own output
	dockerCli, err := command.NewDockerCli(command.WithOutputStream(out), command.WithErrorStream(out))
	if err != nil {
		return nil, err
	}
	if err := dockerCli.Initialize(&flags.ClientOptions{}); err != nil {
		return nil, err
	}
	defer dockerCli.Client().Close()

	backend, err := compose.NewComposeService(dockerCli, compose.WithEventProcessor(display.Plain(out)))
	if err != nil {
		return nil, err
	}

	// buildx when the host has it, the daemon's builder otherwise (compose decides)
	if err := backend.Build(ctx, project, api.BuildOptions{Services: built, Progress: "plain", Out: out}); err != nil {
		return nil, err
	}

	return images, nil
}

func (composeGitRuntime) Retag(ctx context.Context, app, dir, commit string) error {
	project, err := loadGitProject(ctx, app, dir, filepath.Join(dir, ".env"))
	if err != nil {
		return err
	}

	_, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	for _, name := range builtServiceNames(project.Services) {
		target := api.GetImageNameOrDefault(project.Services[name], app)
		source, err := gitImageTag(target, commit)
		if err != nil {
			return err
		}

		if _, err := dockerClient.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target}); err != nil {
			return fmt.Errorf("the image of %s at %s: %w", name, commit[:12], err)
		}
	}

	return nil
}

func (composeGitRuntime) TagRunning(ctx context.Context, app, dir, commit string) (map[string]string, error) {
	project, err := loadGitProject(ctx, app, dir, filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}

	_, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+app),
	})
	if err != nil {
		return nil, err
	}

	images := map[string]string{}
	for _, name := range builtServiceNames(project.Services) {
		for _, c := range containers.Items {
			if c.Labels[api.ServiceLabel] != name || c.Labels[api.OneoffLabel] == "True" {
				continue
			}

			tag, err := gitImageTag(api.GetImageNameOrDefault(project.Services[name], app), commit)
			if err != nil {
				return nil, err
			}
			// by ID: the name may already point at an image built since the container was made
			if _, err := dockerClient.ImageTag(ctx, client.ImageTagOptions{Source: c.ImageID, Target: tag}); err != nil {
				return nil, err
			}
			images[name] = tag

			break
		}
	}

	return images, nil
}

func (composeGitRuntime) Start(ctx context.Context, app, dir string) error {
	files, err := gitComposeFiles(dir)
	if err != nil {
		return err
	}

	composeApp, err := LoadComposeAppFromConfigFile(app, strings.Join(files, ","))
	if err != nil {
		return err
	}

	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	if err := composeApp.Pull(ctx); err != nil {
		return err
	}

	// A wait that only ran out of time goes on to the judgement below, like a start that
	// confirmed: keepNewDefinition keeps such a definition for every other apply, and a
	// deployment is judged by its built services instead.
	if err := composeApp.UpWithCheckRequire(ctx, backend); err != nil && !errors.Is(err, errUpNotConfirmed) {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(gitSettleDelay):
	}

	containers, err := composeApp.Containers(ctx)
	if err != nil {
		return err
	}

	return failedBuiltContainer(containers, builtServiceNames(composeApp.Services))
}

func (composeGitRuntime) Stop(ctx context.Context, app string) error {
	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	return backend.Stop(ctx, app, api.StopOptions{})
}

func (composeGitRuntime) Remove(ctx context.Context, app string) error {
	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	return backend.Down(ctx, app, api.DownOptions{RemoveOrphans: true})
}

func (composeGitRuntime) ImagesExist(ctx context.Context, images map[string]string) bool {
	_, dockerClient, err := apiService()
	if err != nil {
		return false
	}
	defer dockerClient.Close()

	for _, image := range images {
		if _, err := dockerClient.ImageInspect(ctx, image); err != nil {
			return false
		}
	}

	return true
}

func (composeGitRuntime) RemoveImages(ctx context.Context, images []string) {
	_, dockerClient, err := apiService()
	if err != nil {
		logger.Error("cannot remove the images of old deployments", zap.Error(err))
		return
	}
	defer dockerClient.Close()

	for _, image := range images {
		used, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
			All:     true,
			Filters: make(client.Filters).Add("ancestor", image),
		})
		if err != nil || len(used.Items) > 0 {
			continue
		}

		if _, err := dockerClient.ImageRemove(ctx, image, client.ImageRemoveOptions{}); err != nil {
			logger.Info("the image of an old deployment stays", zap.String("image", image), zap.Error(err))
		}
	}
}
