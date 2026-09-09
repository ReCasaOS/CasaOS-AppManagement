package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/inkly/CasaOS-AppManagement/codegen"
	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	"github.com/inkly/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// How many registries to ask at once. Each answer is a token request followed by a
// HEAD, both mostly latency, so the limit is about not looking like a scraper to a
// registry rather than about local CPU.
const imageUpdateConcurrency = 8

// imageUpdates holds two different answers, and telling them apart matters.
//
// `registry` is what the registries said: this image is not the one on disk. That is
// an input to a decision, not the decision.
//
// `offered` is what the update button will actually do. For an app with a catalogue
// entry an update writes the CATALOGUE's compose, so a moved image the catalogue
// would not act on is not an update anyone can take -- and badging it produced
// exactly what a user reported: a badge saying an update was available beside a
// button answering `is up to date`. The dashboard reads this one.
//
// Both are filled by a check pass. The app grid renders on the dashboard's first
// paint and must never be the thing that waits on a registry, so it only ever reads.
var imageUpdates = struct {
	sync.RWMutex
	registry map[string]bool
	offered  map[string]bool
}{registry: map[string]bool{}, offered: map[string]bool{}}

// ImageUpdateAvailable reports whether an update is on offer for an app, or nil if
// nothing has looked since this service started. Nil is not "up to date": the
// dashboard shows a badge for true and nothing for either other case, and conflating
// them would claim an app is current when nobody has looked.
func ImageUpdateAvailable(appName string) *bool {
	imageUpdates.RLock()
	defer imageUpdates.RUnlock()

	updatable, ok := imageUpdates.offered[appName]
	if !ok {
		return nil
	}

	return &updatable
}

// imageUpdatable is the registry's half of the answer, folded to a plain yes or no
// for the decision that combines it with the catalogue. Unknown counts as no: nobody
// has looked, which is not a reason to offer an update.
func imageUpdatable(appName string) bool {
	imageUpdates.RLock()
	defer imageUpdates.RUnlock()

	return imageUpdates.registry[appName]
}

// rememberOffered records what the update button would do for the apps it was asked
// about, leaving every other app's answer alone.
func rememberOffered(offered map[string]bool) {
	imageUpdates.Lock()
	defer imageUpdates.Unlock()

	for name, updatable := range offered {
		imageUpdates.offered[name] = updatable
	}
}

// CheckImageUpdates asks every installed app's registry what its tags point at now
// and compares that with the copy on disk.
//
// This is what sees an app that came from no app store. The store's upgradable list
// compares tags against a catalogue, so it is blind to an app nobody publishes a
// catalogue entry for, and equally blind to a tag that has been republished under
// the same name -- which is what `latest` does every time.
func CheckImageUpdates(ctx context.Context) (*codegen.ImageUpdateCheckResult, error) {
	composeApps, err := MyService.Compose().List(ctx)
	if err != nil {
		return nil, err
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	// What the apps are RUNNING, in one call for every app at once.
	containers, err := containersByService(ctx, cli, "")
	if err != nil {
		return nil, err
	}

	// Several apps can run the same image, and a registry should be asked once for
	// it rather than once per app.
	digests := checkRegistries(ctx, distinctImages(composeApps))

	result := &codegen.ImageUpdateCheckResult{
		Updatable: []string{},
		Unchecked: map[string]string{},
	}

	fresh := make(map[string]bool, len(composeApps))
	for name, composeApp := range composeApps {
		updatable, reason := verdict(ctx, cli, composeApp, containers[name], digests)
		if reason != "" {
			result.Unchecked[name] = reason
			continue
		}

		fresh[name] = updatable
	}

	remember(fresh, composeApps)

	// What the registries said is only half of it. An update writes the catalogue's
	// compose for an app that has an entry, so an image that moved somewhere the
	// catalogue will not follow is not an update anyone can take. Ask what the button
	// would do, and report and badge that.
	offered := make(map[string]bool, len(fresh))
	for name := range fresh {
		MyService.AppStoreManagement().ForgetUpgradable(name)

		if MyService.AppStoreManagement().IsUpdateAvailable(composeApps[name]) {
			offered[name] = true
			result.Updatable = append(result.Updatable, name)

			continue
		}

		offered[name] = false
	}
	sort.Strings(result.Updatable)

	rememberOffered(offered)
	saveImageUpdates()

	return result, nil
}

// CheckImageUpdatesForApp answers for one app and records it, leaving every other
// app's answer alone.
//
// This is what makes the per-app button honest. It is called `Check then update`,
// and for an app that came from no store there is nothing else to check: without
// this it would answer from whatever the last sweep happened to leave in the cache,
// or from nothing at all if no sweep has run.
func CheckImageUpdatesForApp(ctx context.Context, composeApp *ComposeApp) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer cli.Close()

	containers, err := containersByService(ctx, cli, composeApp.Name)
	if err != nil {
		return err
	}

	only := map[string]*ComposeApp{composeApp.Name: composeApp}
	updatable, reason := verdict(ctx, cli, composeApp, containers[composeApp.Name], checkRegistries(ctx, distinctImages(only)))
	if reason != "" {
		return fmt.Errorf("%s: %s", composeApp.Name, reason)
	}

	// scoped, because asking what the button would do reads this same lock
	func() {
		imageUpdates.Lock()
		defer imageUpdates.Unlock()
		imageUpdates.registry[composeApp.Name] = updatable
	}()

	MyService.AppStoreManagement().ForgetUpgradable(composeApp.Name)
	rememberOffered(map[string]bool{
		composeApp.Name: MyService.AppStoreManagement().IsUpdateAvailable(composeApp),
	})
	saveImageUpdates()

	return nil
}

// remember replaces the cache with this pass's answers, keeping the previous answer
// for an app this pass could not check, and dropping apps that are gone.
func remember(fresh map[string]bool, installed map[string]*ComposeApp) {
	imageUpdates.Lock()
	defer imageUpdates.Unlock()

	next := make(map[string]bool, len(fresh))
	for name, updatable := range fresh {
		next[name] = updatable
	}
	for name, updatable := range imageUpdates.registry {
		if _, stillInstalled := installed[name]; !stillInstalled {
			continue
		}
		if _, answered := next[name]; !answered {
			next[name] = updatable
		}
	}

	imageUpdates.registry = next
}

// dockerDaemon is what the update check needs of the daemon: which containers an app
// has, and what image a reference resolves to. An interface, so the branches below
// are testable on a machine where no daemon can start.
type dockerDaemon interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
}

// containersByService lists the containers of one compose project -- or of every
// project, for an empty name -- grouped by app and then by the service owning them.
//
// Not composeApp.Containers: compose's summaries carry the image NAME a container
// was created with, and the image ID is the one field this check exists to read. One
// list call also answers for every app at once, where Ps is a call per app.
func containersByService(ctx context.Context, cli dockerDaemon, project string) (map[string]map[string][]types.Container, error) {
	label := api.ProjectLabel
	if project != "" {
		label += "=" + project
	}

	// All, because a stopped app still has containers and the image they were created
	// from is the one that comes back when it starts. Asking only running ones would
	// send every paused app down the fallback path below.
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", label)),
	})
	if err != nil {
		return nil, err
	}

	byApp := map[string]map[string][]types.Container{}
	for _, c := range containers {
		// a `compose run` leftover is not what the app runs
		if c.Labels[api.OneoffLabel] == "True" {
			continue
		}

		app, service := c.Labels[api.ProjectLabel], c.Labels[api.ServiceLabel]
		if app == "" || service == "" {
			continue
		}

		if byApp[app] == nil {
			byApp[app] = map[string][]types.Container{}
		}
		byApp[app][service] = append(byApp[app][service], c)
	}

	return byApp, nil
}

// runningDigests answers what the registry should be compared against for one
// service: the digests of the image each of its containers was CREATED FROM, one
// entry per distinct image. Replicas made from the same image collapse to one entry;
// replicas that disagree keep one each, because a stale one among them is still
// stale.
//
// The second return says the answer came from the tag on disk instead. That is a
// weaker and different question -- see the caller for why it is logged.
func runningDigests(ctx context.Context, cli dockerDaemon, containers []types.Container, image string) ([][]string, bool) {
	digests := [][]string{}
	seen := map[string]struct{}{}

	for _, c := range containers {
		if _, ok := seen[c.ImageID]; c.ImageID == "" || ok {
			continue
		}
		seen[c.ImageID] = struct{}{}

		local, _, err := cli.ImageInspectWithRaw(ctx, c.ImageID)
		if err != nil {
			// re-tagged or pruned since this container started, so it cannot say what
			// it runs and does not get a vote
			logger.Info("cannot inspect the image a container was created from",
				zap.String("container", c.ID), zap.String("imageID", c.ImageID), zap.Error(err))
			continue
		}

		digests = append(digests, local.RepoDigests)
	}

	if len(digests) > 0 {
		return digests, false
	}

	local, _, err := cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		logger.Info("cannot inspect image, skipping", zap.String("image", image), zap.Error(err))
		return nil, true
	}

	return [][]string{local.RepoDigests}, true
}

// verdict folds an app's services into one answer, comparing what each registry
// publishes now against the image that service's containers were created from. An
// app is updatable as soon as one container is not on the published image -- every
// service of a compose app is recreated together, so one stale container is enough.
//
// A partial "yes" is still an answer: one image is known to have moved, and nothing
// unasked can take that back. A partial "no" is not. If any service could not be
// answered for, the app is unchecked and says which image and why -- an unreachable
// registry is not evidence that nothing changed, and a two-service stack where only
// the reachable half was compared must not report itself up to date.
func verdict(ctx context.Context, cli dockerDaemon, composeApp *ComposeApp, containers map[string][]types.Container, published map[string]registryDigest) (bool, string) {
	var firstReason string
	answered := false

	reason := func(image, why string) {
		if firstReason == "" {
			firstReason = fmt.Sprintf("%s: %s", image, why)
		}
	}

	for _, name := range sortedServiceNames(composeApp.Services) {
		image := composeApp.Services[name].Image
		if image == "" {
			continue
		}

		registry, ok := published[image]
		if !ok {
			reason(image, "nothing asked its registry")
			continue
		}
		if registry.reason != "" {
			reason(image, registry.reason)
			continue
		}

		digests, fromDisk := runningDigests(ctx, cli, containers[name], image)
		if len(digests) == 0 {
			reason(image, "not pulled on this host")
			continue
		}

		if fromDisk {
			// Loud on purpose. This answers "is the copy on my disk the published
			// one", which is NOT the question: a pull that never got recreated -- a
			// failed update, or another tool on the host -- matches on disk while the
			// container keeps running the old image. It is the right answer only while
			// there is no container to ask, so it must not quietly become the usual path.
			logger.Warn("no container to read an image from, comparing the tag on disk instead",
				zap.String("app", composeApp.Name), zap.String("service", name), zap.String("image", image))
		}

		for _, repoDigests := range digests {
			if len(repoDigests) == 0 {
				// built here, loaded from a tar, or pulled before the daemon recorded
				// digests: nothing published to compare against
				reason(image, "built locally, so there is no published digest to compare")
				continue
			}

			answered = true
			if !docker.ContainsDigest(repoDigests, registry.digest) {
				return true, ""
			}
		}
	}

	if firstReason != "" {
		return false, firstReason
	}
	if !answered {
		return false, "this app runs no image with a tag to compare"
	}

	return false, ""
}

// registryDigest is what one registry answered for one image, or why it could not be
// asked. A reason rather than a guess: an unreachable registry is not evidence that
// nothing has changed, and reporting that as up to date is how a host silently stops
// being told about updates.
type registryDigest struct {
	digest string
	reason string
}

func distinctImages(composeApps map[string]*ComposeApp) []string {
	seen := map[string]struct{}{}
	images := []string{}

	for _, composeApp := range composeApps {
		for _, name := range sortedServiceNames(composeApp.Services) {
			image := composeApp.Services[name].Image
			if image == "" {
				continue
			}
			if _, ok := seen[image]; ok {
				continue
			}
			seen[image] = struct{}{}
			images = append(images, image)
		}
	}
	sort.Strings(images)

	return images
}

// checkRegistries asks each registry what its tag points at now.
func checkRegistries(ctx context.Context, images []string) map[string]registryDigest {
	answers := make(map[string]registryDigest, len(images))

	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(imageUpdateConcurrency)

	for _, image := range images {
		image := image
		group.Go(func() error {
			answer := registryDigest{}

			if err := groupCtx.Err(); err != nil {
				answer.reason = "the check was cancelled before its registry was asked"
			} else if digest, err := docker.RegistryDigest(image); err != nil {
				logger.Info("cannot reach registry for image, skipping", zap.String("image", image), zap.Error(err))
				answer.reason = "its registry could not be reached"
			} else {
				answer.digest = digest
			}

			mu.Lock()
			defer mu.Unlock()
			answers[image] = answer

			// One unreachable registry must not abandon the other images, so a
			// failure is recorded rather than returned.
			return nil
		})
	}
	_ = group.Wait()

	return answers
}

// Where the answers live between runs. The dashboard badges an app from this cache,
// so without it every restart blanks the badges until a sweep has run again -- and
// with a check interval measured in hours, that is most of the time.
//
// A var, not a const, only so a test can point it somewhere writable.
var imageUpdateStatePath = "/var/lib/casaos/image_updates.json"

// persistedImageUpdates keeps the two maps apart on disk for the same reason they
// are apart in memory: what the registries said is not what the update button will
// do, and seeding one from the other is how an app gets badged that the button then
// refuses to act on.
type persistedImageUpdates struct {
	Registry map[string]bool `json:"registry"`
	Offered  map[string]bool `json:"offered"`
}

// LoadImageUpdates restores what the last run found, so the dashboard's first paint
// after a restart shows the badges it showed before it.
func LoadImageUpdates() {
	buf, err := os.ReadFile(imageUpdateStatePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Error("cannot read remembered image update answers", zap.String("path", imageUpdateStatePath), zap.Error(err))
		}
		return
	}

	var state persistedImageUpdates
	if err := json.Unmarshal(buf, &state); err != nil {
		logger.Error("remembered image update answers are unreadable, starting with none", zap.String("path", imageUpdateStatePath), zap.Error(err))
		return
	}

	imageUpdates.Lock()
	defer imageUpdates.Unlock()

	if state.Registry != nil {
		imageUpdates.registry = state.Registry
	}
	if state.Offered != nil {
		imageUpdates.offered = state.Offered
	}
}

// saveImageUpdates writes the answers where the next run will find them.
//
// Through a temporary file and a rename, because the file being replaced is one the
// next start reads: a write cut short by a power loss on a home server would leave a
// truncated file where a readable one was, and the badges would come back wrong
// rather than merely absent.
func saveImageUpdates() {
	imageUpdates.RLock()
	buf, err := json.Marshal(persistedImageUpdates{
		Registry: imageUpdates.registry,
		Offered:  imageUpdates.offered,
	})
	imageUpdates.RUnlock()

	if err != nil {
		logger.Error("cannot encode image update answers", zap.Error(err))
		return
	}

	if err := os.MkdirAll(filepath.Dir(imageUpdateStatePath), 0o755); err != nil {
		logger.Error("cannot create the directory for image update answers", zap.String("path", imageUpdateStatePath), zap.Error(err))
		return
	}

	tmpPath := imageUpdateStatePath + ".tmp"
	if err := os.WriteFile(tmpPath, buf, 0o644); err != nil {
		logger.Error("cannot write image update answers", zap.String("path", tmpPath), zap.Error(err))
		return
	}

	if err := os.Rename(tmpPath, imageUpdateStatePath); err != nil {
		logger.Error("cannot store image update answers", zap.String("path", imageUpdateStatePath), zap.Error(err))

		if err := os.Remove(tmpPath); err != nil {
			logger.Error("cannot remove the leftover image update answers", zap.String("path", tmpPath), zap.Error(err))
		}
	}
}
