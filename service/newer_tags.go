package service

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/distribution/reference"
	"go.uber.org/zap"
)

// Newer versions of an image pinned to one.
//
// The update check answers whether `jellyfin:10.9.11` has a new build. An app pinned to
// a version never gets one: the newer version is another tag. This reads the tags the
// registry holds and keeps, for each pinned image, the highest bug-fix, minor and major
// version of the same flavour. It is only ever said: nothing is applied, and a change of
// tag is the owner's, in the app's settings.

// versionTag is a tag read as a version: `v1.4.2-alpine` is the prefix `v`, the numbers
// 1, 4 and 2, and the suffix `-alpine`. raw is the tag as the registry spells it.
type versionTag struct {
	raw     string
	prefix  string
	numbers []int
	suffix  string
}

var versionTagPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})(.*)$`)

// prereleaseWord marks a tag that is not a release: a candidate, a beta, a nightly.
var prereleaseWord = regexp.MustCompile(`(?i)(alpha|beta|rc|pre|preview|dev|nightly|snapshot|test|canary|edge|unstable)`)

// buildCounter is the one part of a suffix that may differ between two tags of a flavour:
// a build number at its very end, `-ls245`, `-r5`, `-build12`, or a suffix that is only a
// number, `-1`. Every other part of a suffix is the flavour itself: an architecture, a
// distribution and its release, a CUDA or a PHP version. A suggestion across those is a
// tag that does not run here.
var buildCounter = regexp.MustCompile(`(?:^[-_.]|[-_.](?:ls|r|build))(\d+)$`)

func parseVersionTag(tag string) (versionTag, bool) {
	match := versionTagPattern.FindStringSubmatch(tag)
	if match == nil {
		return versionTag{}, false
	}
	// a suffix must start apart from the numbers: `1.4.2x` is a word, not a flavour
	if suffix := match[3]; suffix != "" && !strings.ContainsAny(suffix[:1], "-_+.") {
		return versionTag{}, false
	}
	parts := strings.Split(match[2], ".")
	numbers := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return versionTag{}, false
		}
		numbers[i] = n
	}

	return versionTag{raw: tag, prefix: match[1], numbers: numbers, suffix: match[3]}, true
}

// line is what two tags must share to be one line: the prefix, how many numbers, whether
// the first number is a year (repositories that once tagged by date keep those tags, and
// `2021.12.16` is not a major version above `10.9.11`), and the suffix but its build
// counter.
func (v versionTag) line() string {
	dated := "v"
	if v.numbers[0] >= 1900 {
		dated = "d"
	}

	return v.prefix + "|" + strconv.Itoa(len(v.numbers)) + "|" + dated + "|" + buildCounter.ReplaceAllString(v.suffix, "#")
}

// release is the line of the release a prerelease leads to: `2.0.0-beta.1` leads to `2.0.0`.
func (v versionTag) release() string {
	return versionTag{prefix: v.prefix, numbers: v.numbers}.line()
}

func (v versionTag) prerelease() bool { return prereleaseWord.MatchString(v.suffix) }

// build is the build counter at the end of the suffix, or -1 when there is none.
func (v versionTag) build() int {
	if match := buildCounter.FindStringSubmatch(v.suffix); match != nil {
		n, _ := strconv.Atoi(match[1])
		return n
	}

	return -1
}

// compare orders two tags: the numbers, then a release above its prereleases, then the
// build counter, so that `10.9.11-ls246` comes after `10.9.11-ls245`.
func (v versionTag) compare(o versionTag) int {
	for i := range v.numbers {
		if i < len(o.numbers) && v.numbers[i] != o.numbers[i] {
			return v.numbers[i] - o.numbers[i]
		}
	}
	if v.prerelease() != o.prerelease() {
		if v.prerelease() {
			return -1
		}
		return 1
	}

	return v.build() - o.build()
}

// newerTagsOf is, among tags, the highest of each level above current, of its line, as
// the registry spells them. A level is empty when nothing is above. Prereleases are left
// out, unless current is one, which is also offered the release it leads to. Nothing
// when current is not a version.
func newerTagsOf(current string, tags []string) (patch, minor, major string) {
	now, ok := parseVersionTag(current)
	if !ok {
		return "", "", ""
	}
	best := map[string]versionTag{}
	for _, tag := range tags {
		candidate, ok := parseVersionTag(tag)
		if !ok || candidate.compare(now) <= 0 {
			continue
		}
		sameLine := candidate.line() == now.line()
		toRelease := now.prerelease() && candidate.suffix == "" && candidate.line() == now.release()
		if !sameLine && !toRelease {
			continue
		}
		if candidate.prerelease() && !now.prerelease() {
			continue
		}
		level := "patch"
		switch {
		case candidate.numbers[0] != now.numbers[0]:
			level = "major"
		case len(now.numbers) > 1 && candidate.numbers[1] != now.numbers[1]:
			level = "minor"
		}
		if was, seen := best[level]; !seen || candidate.compare(was) > 0 {
			best[level] = candidate
		}
	}

	return best["patch"].raw, best["minor"].raw, best["major"].raw
}

// The last pass, by app. Kept in memory: a restart asks again a few minutes later.
var newerTags struct {
	sync.RWMutex
	apps map[string][]codegen.NewerTag
}

// NewerTagsOf is what the last pass found for an app, for the images it names now: an
// entry about an image the app no longer runs, its tag changed since, is left out. Nil
// when nothing applies.
func NewerTagsOf(app string, images map[string]string) []codegen.NewerTag {
	newerTags.RLock()
	defer newerTags.RUnlock()

	var out []codegen.NewerTag
	for _, entry := range newerTags.apps[app] {
		if images[entry.Service] == entry.Image {
			out = append(out, entry)
		}
	}

	return out
}

// listTags is a variable so a test asks no registry.
var listTags = docker.ListTags

// pinnedImages is, for every app, the images it pulls that are pinned to a version. An
// image a service builds is left out: its name is local, and a public repository of the
// same name is someone else's. A variable so a test installs what it likes.
var pinnedImages = func(ctx context.Context) (map[string]map[string]string, error) {
	apps, err := MyService.Compose().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for name, app := range apps {
		for _, service := range pulledServiceNames(app.Services) {
			image := app.Services[service].Image
			if image == "" || strings.Contains(image, "@") {
				continue
			}
			if _, tag := docker.ExtractImageAndTag(image); tag != "" {
				if _, ok := parseVersionTag(tag); ok {
					if out[name] == nil {
						out[name] = map[string]string{}
					}
					out[name][service] = image
				}
			}
		}
	}

	return out, nil
}

// repositoryKey is one key for every spelling of a repository: `postgres` and
// `docker.io/library/postgres` are read once.
func repositoryKey(image string) string {
	if named, err := reference.ParseNormalizedNamed(image); err == nil {
		return named.Name()
	}
	repository, _ := docker.ExtractImageAndTag(image)

	return repository
}

// CheckNewerTags reads the tags of every pinned image once, and keeps what it found. A
// registry that does not answer keeps what the last pass found for its images.
func CheckNewerTags(ctx context.Context) {
	images, err := pinnedImages(ctx)
	if err != nil {
		logger.Error("newer versions: the apps cannot be listed", zap.Error(err))
		return
	}

	newerTags.RLock()
	previous := newerTags.apps
	newerTags.RUnlock()

	// one request per repository, whatever number of apps run it
	byRepository := map[string][]string{}
	failed := map[string]bool{}
	found := map[string][]codegen.NewerTag{}

	apps := make([]string, 0, len(images))
	for app := range images {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	for _, app := range apps {
		services := make([]string, 0, len(images[app]))
		for service := range images[app] {
			services = append(services, service)
		}
		sort.Strings(services)
		for _, service := range services {
			image := images[app][service]
			key := repositoryKey(image)
			tags, known := byRepository[key]
			if !known && !failed[key] {
				var err error
				if tags, err = listTags(ctx, image); err != nil {
					logger.Info("newer versions: the tags cannot be read", zap.String("image", image), zap.Error(err))
					failed[key] = true
				} else {
					byRepository[key] = tags
				}
			}
			if failed[key] {
				// what the last pass found for this very image stands until the next one
				for _, entry := range previous[app] {
					if entry.Service == service && entry.Image == image {
						found[app] = append(found[app], entry)
					}
				}
				continue
			}

			_, current := docker.ExtractImageAndTag(image)
			patch, minor, major := newerTagsOf(current, tags)
			if patch == "" && minor == "" && major == "" {
				continue
			}
			entry := codegen.NewerTag{Service: service, Image: image, Current: current}
			if patch != "" {
				entry.Patch = &patch
			}
			if minor != "" {
				entry.Minor = &minor
			}
			if major != "" {
				entry.Major = &major
			}
			found[app] = append(found[app], entry)
		}
	}

	newerTags.Lock()
	newerTags.apps = found
	newerTags.Unlock()
	logger.Info("newer versions checked", zap.Int("apps", len(found)), zap.Time("at", time.Now()))
}
