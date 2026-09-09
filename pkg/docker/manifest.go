/*
credit: https://github.com/containrrr/watchtower
*/
package docker

import (
	"fmt"
	"strings"

	url2 "net/url"

	ref "github.com/distribution/reference"
)

// BuildManifestURL from raw image data
func BuildManifestURL(imageName string) (string, error) {
	normalizedName, err := ref.ParseNormalizedNamed(imageName)
	if err != nil {
		return "", err
	}

	host, err := NormalizeRegistry(normalizedName.String())
	img, tag := ExtractImageAndTag(strings.TrimPrefix(imageName, host+"/"))

	if err != nil {
		return "", err
	}
	img = GetScopeFromImageName(img, host)

	if !strings.Contains(img, "/") {
		img = "library/" + img
	}
	url := url2.URL{
		Scheme: "https",
		Host:   host,
		Path:   fmt.Sprintf("/v2/%s/manifests/%s", img, tag),
	}
	return url.String(), nil
}

// ExtractImageAndTag splits a reference into the image and its tag.
//
// The colon that separates a tag is the last one AFTER the last slash. A registry
// carries a port before the first slash -- `registry:5000/img:2.0` -- and splitting
// on the first colon returned `registry` and `5000/img:2.0`. That string is not a
// version, so the update check treated every such app as unorderable and offered it
// whatever the catalogue held, in either direction.
//
// A reference pinned by digest has no tag at all, and comes back whole with an empty
// tag rather than split at the digest's own colon.
func ExtractImageAndTag(imageName string) (string, string) {
	if at := strings.LastIndex(imageName, "@"); at >= 0 {
		return imageName, ""
	}

	colon := strings.LastIndex(imageName, ":")
	if colon < 0 || colon < strings.LastIndex(imageName, "/") {
		return imageName, "latest"
	}

	return imageName[:colon], imageName[colon+1:]
}
