package service

import (
	"context"
	"fmt"
	"sort"
	"sync"

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

	// Several apps can run the same image, and a registry should be asked once for
	// it rather than once per app.
	digests := checkImages(ctx, cli, distinctImages(composeApps))

	result := &codegen.ImageUpdateCheckResult{
		Updatable: []string{},
		Unchecked: map[string]string{},
	}

	fresh := make(map[string]bool, len(composeApps))
	for name, composeApp := range composeApps {
		updatable, reason := verdict(composeApp, digests)
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

	only := map[string]*ComposeApp{composeApp.Name: composeApp}
	updatable, reason := verdict(composeApp, checkImages(ctx, cli, distinctImages(only)))
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

// verdict folds an app's images into one answer. An app is updatable as soon as one
// of its images has moved -- every service of a compose app is recreated together,
// so one stale image is enough. It is unchecked only when none of its images could
// be answered for, because a partial answer of "yes" is still an answer.
func verdict(composeApp *ComposeApp, digests map[string]imageVerdict) (bool, string) {
	var firstReason string
	answered := false

	for _, name := range sortedServiceNames(composeApp.Services) {
		image := composeApp.Services[name].Image
		if image == "" {
			continue
		}

		v, ok := digests[image]
		if !ok || v.reason != "" {
			if firstReason == "" {
				firstReason = fmt.Sprintf("%s: %s", image, v.reason)
			}
			continue
		}

		answered = true
		if v.updatable {
			return true, ""
		}
	}

	if !answered {
		if firstReason == "" {
			firstReason = "this app runs no image with a tag to compare"
		}
		return false, firstReason
	}

	return false, ""
}

type imageVerdict struct {
	updatable bool
	reason    string
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

func checkImages(ctx context.Context, cli client.APIClient, images []string) map[string]imageVerdict {
	verdicts := make(map[string]imageVerdict, len(images))

	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(imageUpdateConcurrency)

	for _, image := range images {
		image := image
		group.Go(func() error {
			updatable, reason := checkImage(groupCtx, cli, image)

			mu.Lock()
			defer mu.Unlock()
			verdicts[image] = imageVerdict{updatable: updatable, reason: reason}

			// One unreachable registry must not abandon the other images, so a
			// failure is recorded rather than returned.
			return nil
		})
	}
	_ = group.Wait()

	return verdicts
}

// checkImage compares one image against its registry. It answers with a reason
// rather than a guess whenever it cannot tell: an unreachable registry is not
// evidence that nothing has changed, and reporting that as up to date is how a host
// silently stops being told about updates.
func checkImage(ctx context.Context, cli client.APIClient, image string) (bool, string) {
	local, _, err := cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		logger.Info("cannot inspect image, skipping", zap.String("image", image), zap.Error(err))
		return false, "not pulled on this host"
	}

	if len(local.RepoDigests) == 0 {
		// Built here, loaded from a tar, or pulled before the daemon recorded
		// digests. There is nothing to compare a registry answer against.
		return false, "built locally, so there is no published digest to compare"
	}

	match, err := docker.CompareDigest(image, local.RepoDigests)
	if err != nil {
		logger.Info("cannot reach registry for image, skipping", zap.String("image", image), zap.Error(err))
		return false, "its registry could not be reached"
	}

	return !match, ""
}
