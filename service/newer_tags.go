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
// 1, 4 and 2, and the suffix `-alpine`.
type versionTag struct {
	prefix  string
	numbers []int
	suffix  string
}

var versionTagPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})(.*)$`)

// prereleaseWord marks a tag that is not a release: a candidate, a beta, a nightly.
var prereleaseWord = regexp.MustCompile(`(?i)(alpha|beta|rc|pre|preview|dev|nightly|snapshot|test|canary|edge|unstable)`)

var digits = regexp.MustCompile(`\d+`)

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

	return versionTag{prefix: match[1], numbers: numbers, suffix: match[3]}, true
}

// flavour is what two tags must share to be one line: the prefix, how many numbers, the
// suffix with its own numbers set aside, so `-ls245` and `-ls246` are one flavour and
// `-alpine` another, and whether the first number is a year. Repositories that once
// tagged by date keep those tags: `2021.12.16` is not a major version above `10.9.11`.
func (v versionTag) flavour() string {
	dated := "v"
	if v.numbers[0] >= 1900 {
		dated = "d"
	}

	return v.prefix + "|" + strconv.Itoa(len(v.numbers)) + "|" + dated + "|" + digits.ReplaceAllString(v.suffix, "#")
}

func (v versionTag) prerelease() bool { return prereleaseWord.MatchString(v.suffix) }

// compare orders two tags of one flavour: the numbers, then the numbers of the suffix,
// so that `10.9.11-ls246` comes after `10.9.11-ls245`.
func (v versionTag) compare(o versionTag) int {
	for i := range v.numbers {
		if v.numbers[i] != o.numbers[i] {
			return v.numbers[i] - o.numbers[i]
		}
	}
	a, b := digits.FindAllString(v.suffix, -1), digits.FindAllString(o.suffix, -1)
	for i := 0; i < len(a) && i < len(b); i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x - y
		}
	}

	return 0
}

// newerTagsOf is, among tags, the highest of each level above current, of its flavour.
// A level is empty when nothing is above. Prereleases are left out, unless current is
// one. Nothing when current is not a version.
func newerTagsOf(current string, tags []string) (patch, minor, major string) {
	now, ok := parseVersionTag(current)
	if !ok {
		return "", "", ""
	}
	best := map[string]versionTag{}
	for _, tag := range tags {
		candidate, ok := parseVersionTag(tag)
		if !ok || candidate.flavour() != now.flavour() || candidate.compare(now) <= 0 {
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
	text := func(v versionTag, seen bool) string {
		if !seen {
			return ""
		}
		parts := make([]string, len(v.numbers))
		for i, n := range v.numbers {
			parts[i] = strconv.Itoa(n)
		}

		return v.prefix + strings.Join(parts, ".") + v.suffix
	}
	p, hasP := best["patch"]
	m, hasM := best["minor"]
	M, hasMajor := best["major"]

	return text(p, hasP), text(m, hasM), text(M, hasMajor)
}

// The last pass, by app. Kept in memory: a restart asks again a few minutes later.
var newerTags struct {
	sync.RWMutex
	apps map[string][]codegen.NewerTag
}

// NewerTagsOf is what the last pass found for an app: one entry per pinned image that has
// a newer version. Nil when nothing was found, or nothing has been asked yet.
func NewerTagsOf(app string) []codegen.NewerTag {
	newerTags.RLock()
	defer newerTags.RUnlock()

	return newerTags.apps[app]
}

// listTags is a variable so a test asks no registry.
var listTags = docker.ListTags

// pinnedImages is, for every app, its services' images pinned to a version. A variable
// so a test installs what it likes.
var pinnedImages = func(ctx context.Context) (map[string]map[string]string, error) {
	apps, err := MyService.Compose().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for name, app := range apps {
		for service, s := range app.Services {
			if s.Image == "" || strings.Contains(s.Image, "@") {
				continue
			}
			if _, tag := docker.ExtractImageAndTag(s.Image); tag != "" {
				if _, ok := parseVersionTag(tag); ok {
					if out[name] == nil {
						out[name] = map[string]string{}
					}
					out[name][service] = s.Image
				}
			}
		}
	}

	return out, nil
}

// CheckNewerTags reads the tags of every pinned image once, and keeps what it found. A
// registry that does not answer leaves its images out of this pass.
func CheckNewerTags(ctx context.Context) {
	images, err := pinnedImages(ctx)
	if err != nil {
		logger.Error("newer versions: the apps cannot be listed", zap.Error(err))
		return
	}

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
			repository, current := docker.ExtractImageAndTag(image)
			tags, known := byRepository[repository]
			if !known && !failed[repository] {
				var err error
				if tags, err = listTags(ctx, image); err != nil {
					logger.Info("newer versions: the tags cannot be read", zap.String("image", image), zap.Error(err))
					failed[repository] = true
					continue
				}
				byRepository[repository] = tags
			}
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
