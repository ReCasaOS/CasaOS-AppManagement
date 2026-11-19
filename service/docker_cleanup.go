package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// What apps left behind on the disk, and nothing an installed app could still want.
//
// Docker's own prunes judge by containers: an image or a volume no container holds is
// "unused". On this box that is wrong twice. A stopped app is a normal state, and an app
// whose containers are gone for a moment (an update between its down and its up, an app
// waiting for its storage) still owns its images and its data. So everything here is
// judged against the apps that are installed, and a doubt keeps the resource: a count the
// daemon could not give, a compose file that does not load, an operation that runs.

// ErrCleanupHeld is why images and volumes are not offered, or not deleted, right now.
// Detail names what could not be read, for the owner who has to mend it.
type ErrCleanupHeld struct{ Reason, Detail string }

func (e ErrCleanupHeld) Error() string {
	if e.Detail == "" {
		return e.Reason
	}

	return e.Reason + ": " + e.Detail
}

// The two reasons the report carries in `held`.
const (
	CleanupHeldBusy       = "busy"
	CleanupHeldUnreadable = "unreadable"
)

// cleanupProtection is what the installed apps claim, whether or not a container of
// theirs exists right now.
type cleanupProtection struct {
	// compose project names: a volume labelled with one is the app's
	Projects map[string]struct{}
	// image references, as imageKeys writes them
	Images map[string]struct{}
	// repositories of the images compose builds for a service that names none
	Repos map[string]struct{}
	// volume names, as Docker knows them
	Volumes map[string]struct{}
}

// imageKeys is a reference as the keys two spellings of it share: `nginx` and
// `docker.io/library/nginx:latest` are one image. A reference with a tag and a digest
// gives both keys. An error means the reference cannot be read, which protects.
func imageKeys(ref string) (repo string, keys []string, err error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", nil, err
	}
	repo = named.Name()
	if digested, ok := named.(reference.Digested); ok {
		keys = append(keys, repo+"@"+digested.Digest().String())
	}
	if tagged, ok := reference.TagNameOnly(named).(reference.Tagged); ok {
		// TagNameOnly leaves a digest-only reference without a tag
		keys = append(keys, repo+":"+tagged.Tag())
	}

	return repo, keys, nil
}

// protect adds what one loaded app claims.
func (p *cleanupProtection) protect(project *types.Project) error {
	p.Projects[project.Name] = struct{}{}
	for name, service := range project.Services {
		if service.Image == "" {
			// compose names what it builds after the project and the service
			for _, built := range []string{project.Name + "-" + name, project.Name + "_" + name} {
				if repo, _, err := imageKeys(built); err == nil {
					p.Repos[repo] = struct{}{}
				}
			}

			continue
		}
		repo, keys, err := imageKeys(service.Image)
		if err != nil {
			return fmt.Errorf("image `%s` of `%s`: %w", service.Image, project.Name, err)
		}
		if service.Build != nil {
			p.Repos[repo] = struct{}{}
		}
		for _, key := range keys {
			p.Images[key] = struct{}{}
		}
	}
	for key, volume := range project.Volumes {
		p.Volumes[volume.Name] = struct{}{}
		p.Volumes[project.Name+"_"+key] = struct{}{}
	}

	return nil
}

// appsProtection reads what every installed app claims: the stacks Docker Compose knows,
// the app folders on the disk, which is where an app without a container still is, and
// the apps built from a repository, whose files are in their clone. Any file that does
// not load is an error: what it claims is unknown, so nothing is offered.
var appsProtection = func(ctx context.Context) (cleanupProtection, error) {
	protection := cleanupProtection{
		Projects: map[string]struct{}{}, Images: map[string]struct{}{},
		Repos: map[string]struct{}{}, Volumes: map[string]struct{}{},
	}

	stacks, err := ListComposeStacks(ctx)
	if err != nil {
		return protection, err
	}
	files := map[string]string{}
	for _, stack := range stacks {
		protection.Projects[stack.ID] = struct{}{}
		files[stack.ID] = stack.ConfigFiles
	}

	// A missing folder is an error too: with no app at all on the disk, every image
	// would belong to nobody.
	folders, err := os.ReadDir(config.AppInfo.AppsPath)
	if err != nil {
		return protection, err
	}
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		protection.Projects[folder.Name()] = struct{}{}
		if _, known := files[folder.Name()]; known {
			continue
		}
		file := filepath.Join(config.AppInfo.AppsPath, folder.Name(), common.ComposeYAMLFileName)
		if _, err := os.Stat(file); err == nil {
			files[folder.Name()] = file
		} else if !errors.Is(err, os.ErrNotExist) {
			return protection, err
		}
	}

	gitApps, err := GitAppNames()
	if err != nil {
		return protection, err
	}
	for _, name := range gitApps {
		protection.Projects[name] = struct{}{}
		if _, known := files[name]; known {
			continue
		}
		st, err := loadGitApp(name)
		if err != nil {
			return protection, fmt.Errorf("repository app `%s`: %w", name, err)
		}
		composeFiles, err := gitComposeFiles(st.Dir, filepath.Join(st.Dir, ".env"))
		if err != nil {
			return protection, fmt.Errorf("repository app `%s`: %w", name, err)
		}
		files[name] = strings.Join(composeFiles, ",")
	}

	for name, file := range files {
		app, err := LoadComposeAppFromConfigFile(name, file)
		if err != nil {
			return protection, fmt.Errorf("compose file of `%s`: %w", name, err)
		}
		if err := protection.protect((*types.Project)(app)); err != nil {
			return protection, err
		}
	}

	return protection, nil
}

// leftoverImages is the tagged images no container holds and no installed app names,
// biggest first. A dangling image is not listed: it is an old app version, counted apart.
func leftoverImages(images []*image.Summary, protection cleanupProtection) []codegen.CleanupImage {
	out := []codegen.CleanupImage{}
	for _, img := range images {
		// -1 is "not counted": only a known zero is nobody's
		if img == nil || img.Containers != 0 {
			continue
		}
		tags := []string{}
		for _, tag := range img.RepoTags {
			if tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		if len(tags) == 0 || imageClaimed(append(append([]string{}, tags...), img.RepoDigests...), protection) {
			continue
		}
		sort.Strings(tags)

		size := img.Size
		if img.SharedSize > 0 {
			size -= img.SharedSize
		}
		out = append(out, codegen.CleanupImage{Id: img.ID, Tags: tags, Size: size, Created: img.Created})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })

	return out
}

// imageClaimed: an installed app names one of the references, or one cannot be read, or
// it is the image of a deployment from a repository, which that code removes itself.
func imageClaimed(refs []string, protection cleanupProtection) bool {
	for _, ref := range refs {
		if strings.Contains(ref, "<none>") {
			continue
		}
		repo, keys, err := imageKeys(ref)
		if err != nil {
			return true
		}
		if _, built := protection.Repos[repo]; built {
			return true
		}
		for _, key := range keys {
			if _, named := protection.Images[key]; named || strings.HasPrefix(key, repo+":git-") {
				return true
			}
		}
	}

	return false
}

// leftoverVolumes is the volumes no container holds and no installed app owns, biggest
// first. App is the compose project that made it, when Docker kept the label.
func leftoverVolumes(usage dockertypes.DiskUsage, protection cleanupProtection) []codegen.CleanupVolume {
	out := []codegen.CleanupVolume{}
	for _, volume := range usage.Volumes {
		// no usage data, or -1: not counted, so not nobody's
		if volume == nil || volume.Name == "" || volume.UsageData == nil || volume.UsageData.RefCount != 0 {
			continue
		}
		app := volume.Labels["com.docker.compose.project"]
		if _, owned := protection.Projects[app]; owned && app != "" {
			continue
		}
		if _, declared := protection.Volumes[volume.Name]; declared {
			continue
		}

		left := codegen.CleanupVolume{Name: volume.Name, Size: volume.UsageData.Size}
		if app != "" {
			left.App = &app
		}
		if created, err := time.Parse(time.RFC3339, volume.CreatedAt); err == nil {
			left.Created = &created
		}
		out = append(out, left)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })

	return out
}

// buildCacheSize is what a prune of the build cache would free: the records no build uses
// right now. A shared record is counted once by the daemon, so this is not exact.
func buildCacheSize(records []*build.CacheRecord) int64 {
	var total int64
	for _, record := range records {
		if record != nil && !record.InUse && !record.Shared {
			total += record.Size
		}
	}

	return total
}

// cleanupDaemon is what the cleanup needs of the daemon. An interface, so a test can read
// back what was removed: the list of candidates is worth nothing if only it is tested.
type cleanupDaemon interface {
	DiskUsage(ctx context.Context, options dockertypes.DiskUsageOptions) (dockertypes.DiskUsage, error)
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	BuildCachePrune(ctx context.Context, opts build.CachePruneOptions) (*build.CachePruneReport, error)
}

func newCleanupDaemon() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

// leftovers reads the daemon and the apps once, for the kinds of thing asked: measuring
// every volume walks its files, and a deletion of images has no use for it. held is why
// images and volumes are not listed, when they are not; the build cache holds no data of
// any app and is always measured.
//
// holding is true for a caller that took beginCleanup: nothing else can run then, and
// the hold it sees is its own.
func leftovers(ctx context.Context, cli cleanupDaemon, holding bool, kinds ...dockertypes.DiskUsageObject) (usage dockertypes.DiskUsage, images []codegen.CleanupImage, volumes []codegen.CleanupVolume, held ErrCleanupHeld, err error) {
	usage, err = cli.DiskUsage(ctx, dockertypes.DiskUsageOptions{Types: kinds})
	if err != nil {
		return usage, nil, nil, held, err
	}
	images, volumes = []codegen.CleanupImage{}, []codegen.CleanupVolume{}

	// an install, an update, a restore or a build has images and volumes no container
	// holds yet
	if reason := cleanupHeld(); reason != "" && !holding {
		return usage, images, volumes, ErrCleanupHeld{Reason: reason}, nil
	}
	protection, err := appsProtection(ctx)
	if err != nil {
		// said in the log and in the report: one file that does not load holds
		// everything, and its name is the only way to mend it
		logger.Error("cleanup: what an app claims cannot be read, nothing is offered", zap.Error(err))

		return usage, images, volumes, ErrCleanupHeld{Reason: CleanupHeldUnreadable, Detail: err.Error()}, nil
	}

	return usage, leftoverImages(usage.Images, protection), leftoverVolumes(usage, protection), held, nil
}

// DockerCleanup reports what a cleanup could free, and frees nothing.
func DockerCleanup(ctx context.Context) (*codegen.DockerCleanup, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	return dockerCleanup(ctx, cli)
}

func dockerCleanup(ctx context.Context, cli cleanupDaemon) (*codegen.DockerCleanup, error) {
	usage, images, volumes, held, err := leftovers(ctx, cli, false, dockertypes.ImageObject, dockertypes.VolumeObject, dockertypes.BuildCacheObject)
	if err != nil {
		return nil, err
	}

	report := &codegen.DockerCleanup{
		BuildCache: buildCacheSize(usage.BuildCache),
		Images:     images,
		Volumes:    volumes,
		Held:       held.Reason,
	}
	if held.Detail != "" {
		report.Detail = &held.Detail
	}

	return report, nil
}

// PruneBuildCache deletes the build cache no build uses and reports what the daemon freed.
func PruneBuildCache(ctx context.Context) (int64, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return 0, err
	}
	defer cli.Close()

	return pruneBuildCache(ctx, cli)
}

// Held like the rest: a build that runs is about to use what this would delete.
func pruneBuildCache(ctx context.Context, cli cleanupDaemon) (int64, error) {
	end, err := beginCleanup()
	if err != nil {
		return 0, err
	}
	defer end()

	report, err := cli.BuildCachePrune(ctx, build.CachePruneOptions{All: true})
	if err != nil || report == nil {
		return 0, err
	}

	return int64(report.SpaceReclaimed), nil
}

// RemoveLeftoverImages deletes the images asked for that are still leftovers when it runs.
func RemoveLeftoverImages(ctx context.Context, ids []string) (*codegen.CleanupResult, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	return removeLeftoverImages(ctx, cli, ids)
}

// The list is decided again here, against what the daemon and the apps say now: what the
// screen was drawn from may be minutes old, and an app may have been installed since.
func removeLeftoverImages(ctx context.Context, cli cleanupDaemon, ids []string) (*codegen.CleanupResult, error) {
	end, err := beginCleanup()
	if err != nil {
		return nil, err
	}
	defer end()

	_, images, _, held, err := leftovers(ctx, cli, true, dockertypes.ImageObject)
	if err != nil {
		return nil, err
	}
	if held.Reason != "" {
		return nil, held
	}
	offered := map[string]codegen.CleanupImage{}
	for _, img := range images {
		offered[img.Id] = img
	}

	result := &codegen.CleanupResult{Removed: []string{}, Refused: map[string]string{}}
	for _, id := range ids {
		img, ok := offered[id]
		if !ok {
			result.Refused[id] = "not a leftover any more"
			continue
		}
		// By each of its names, never by id: an image with two names is refused by id
		// unless forced, and nothing here forces. The last name takes the image with it,
		// which the daemon says with a Deleted line; without one the image is still
		// there, under a name this did not know.
		deleted, untagged := false, 0
		var failed error
		for _, tag := range img.Tags {
			answers, err := cli.ImageRemove(ctx, tag, image.RemoveOptions{})
			if err != nil {
				failed = err
				break
			}
			untagged++
			for _, answer := range answers {
				deleted = deleted || answer.Deleted != ""
			}
		}
		switch {
		case failed != nil && untagged > 0:
			result.Refused[id] = fmt.Sprintf("%s (%d of its names were removed)", failed, untagged)
		case failed != nil:
			result.Refused[id] = failed.Error()
		case !deleted:
			result.Refused[id] = "its names were removed, the image is still referenced"
		default:
			result.Removed = append(result.Removed, id)
			result.Size += img.Size
		}
	}

	return result, nil
}

// RemoveLeftoverVolumes deletes the volumes asked for that are still leftovers when it runs.
func RemoveLeftoverVolumes(ctx context.Context, names []string) (*codegen.CleanupResult, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	return removeLeftoverVolumes(ctx, cli, names)
}

func removeLeftoverVolumes(ctx context.Context, cli cleanupDaemon, names []string) (*codegen.CleanupResult, error) {
	end, err := beginCleanup()
	if err != nil {
		return nil, err
	}
	defer end()

	_, _, volumes, held, err := leftovers(ctx, cli, true, dockertypes.VolumeObject)
	if err != nil {
		return nil, err
	}
	if held.Reason != "" {
		return nil, held
	}
	offered := map[string]codegen.CleanupVolume{}
	for _, volume := range volumes {
		offered[volume.Name] = volume
	}

	result := &codegen.CleanupResult{Removed: []string{}, Refused: map[string]string{}}
	for _, name := range names {
		volume, ok := offered[name]
		if !ok {
			result.Refused[name] = "not a leftover any more"
			continue
		}
		// never forced: a container that took the volume since keeps it
		if err := cli.VolumeRemove(ctx, name, false); err != nil {
			result.Refused[name] = err.Error()
			continue
		}
		result.Removed = append(result.Removed, name)
		if volume.Size > 0 {
			result.Size += volume.Size
		}
	}

	return result, nil
}
