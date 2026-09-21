package downloadHelper

import (
	"context"

	"github.com/hashicorp/go-getter"
)

// Download fetches src into dst and unpacks it when it is an archive.
//
// An app store URL is typed by the user and the server behind it is not
// trusted, so only http and https are fetched: no file, git, hg, s3 or gcs
// getter, no X-Terraform-Get or <meta name="terraform-get"> redirect to one,
// no .netrc credentials sent to whatever host the URL names, and symlinks
// are never copied.
func Download(src string, dst string) error {
	httpGetter := &getter.HttpGetter{XTerraformGetDisabled: true}
	client := &getter.Client{
		Ctx:             context.Background(),
		Src:             src,
		Dst:             dst,
		Mode:            getter.ClientModeAny,
		DisableSymlinks: true,
		// an option rather than the Getters field, so a nested Get inherits it
		Options: []getter.ClientOption{getter.WithGetters(map[string]getter.Getter{
			"http":  httpGetter,
			"https": httpGetter,
		})},
	}

	return client.Get()
}
