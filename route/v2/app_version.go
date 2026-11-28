package v2

import (
	"net/url"
	"strings"

	"github.com/distribution/reference"
	"github.com/docker/compose/v5/pkg/api"
)

const (
	labelImageVersion = "org.opencontainers.image.version"
	labelImageSource  = "org.opencontainers.image.source"
)

// appVersionOf says what the card shows of the version an app runs. A container's labels
// hold its image's labels, so this costs no call to Docker beyond the list the grid made.
func appVersionOf(containers []api.ContainerSummary, image string) (version, releaseNotes *string) {
	labels := map[string]string{}
	if len(containers) > 0 {
		labels = containers[0].Labels
	}

	if v := strings.TrimSpace(labels[labelImageVersion]); v != "" {
		version = &v
	} else if named, err := reference.ParseNormalizedNamed(image); err == nil {
		if tagged, ok := named.(reference.Tagged); ok && tagged.Tag() != "latest" {
			v := tagged.Tag()
			version = &v
		}
	}

	if notes := githubReleases(labels[labelImageSource]); notes != "" {
		releaseNotes = &notes
	}

	return version, releaseNotes
}

// githubReleases is the releases page of a https://github.com/<owner>/<repo> source, and ""
// for anything else: the label is the image author's text, opened by the dashboard.
// shortcut: GitHub only, add GitLab/Codeberg when someone asks.
func githubReleases(source string) string {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	for _, p := range []string{parts[0], repo} {
		if p == "" || strings.ContainsAny(p, "?#%") {
			return ""
		}
	}

	return "https://github.com/" + parts[0] + "/" + repo + "/releases"
}
