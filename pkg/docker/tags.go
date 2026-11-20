package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The tags a registry holds for a repository, for an image pinned to a version: the
// update check asks whether `1.4.2` has a new build, this asks whether `1.4.3` exists.

// tagsPagesMax bounds how many pages are read for one repository. A thousand tags a page:
// repositories that tag every build hold tens of thousands, and the newest versions are
// not reliably on the first pages, so this reads a fair amount, and no more.
const tagsPagesMax = 20

// ListTags is every tag the registry holds for the image's repository, read with the same
// token as the update check. The tag or digest of imageName is ignored.
func ListTags(ctx context.Context, imageName string) ([]string, error) {
	token, manifestURL, err := tokenAndURL(imageName)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("could not fetch token")
	}
	next, err := tagsListURL(manifestURL)
	if err != nil {
		return nil, err
	}

	tags := []string{}
	for page := 0; page < tagsPagesMax && next != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Add("Authorization", token)

		res, err := httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		decodeErr := json.NewDecoder(res.Body).Decode(&body)
		link := res.Header.Get("Link")
		status := res.Status
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("registry responded to the tag list with %q", status)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("not a tag list: %w", decodeErr)
		}
		tags = append(tags, body.Tags...)

		if next, err = nextTagsPage(next, link); err != nil {
			return nil, err
		}
	}

	return tags, nil
}

// tagsListURL is the tag list of the repository a manifest URL names, a thousand a page.
func tagsListURL(manifestURL string) (string, error) {
	parsed, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	at := strings.LastIndex(parsed.Path, "/manifests/")
	if at < 0 {
		return "", fmt.Errorf("not a manifest URL: %s", manifestURL)
	}
	parsed.Path = parsed.Path[:at] + "/tags/list"
	parsed.RawQuery = url.Values{"n": {"1000"}}.Encode()

	return parsed.String(), nil
}

// nextTagsPage reads the next page from a Link header, `<path?last=x&n=1000>; rel="next"`,
// relative to the page just read. Empty when there is none.
func nextTagsPage(current, link string) (string, error) {
	for _, part := range strings.Split(link, ",") {
		target, params, found := strings.Cut(strings.TrimSpace(part), ";")
		if !found || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		base, err := url.Parse(current)
		if err != nil {
			return "", err
		}
		ref, err := url.Parse(target)
		if err != nil {
			return "", err
		}
		// a registry must not send this check somewhere else
		resolved := base.ResolveReference(ref)
		if resolved.Host != base.Host || resolved.Scheme != base.Scheme {
			return "", fmt.Errorf("the next page of the tag list is on another host: %s", resolved.Host)
		}

		return resolved.String(), nil
	}

	return "", nil
}
