package service

import (
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// The catalogue's mirror.
//
// The App Store is IceWhale's catalogue, fetched as a zip of a GitHub repository
// that nobody maintains any more. The day that repository goes away, every box
// keeps the copy it already has and never sees an update again, and a fresh
// install gets the seed the installer ships and nothing after. This
// distribution keeps a copy of that repository, refreshed every night; when the
// original stops answering, the copy is fetched instead. The configured URL is
// not changed, on disk or in the dashboard: the original is still the source,
// and it is tried first on every update.

// catalogueMirrors maps a catalogue's URL to this distribution's copy of it.
var catalogueMirrors = map[string]string{
	"https://github.com/IceWhaleTech/_appstore/archive/refs/heads/main.zip": "https://github.com/ReCasaOS/_appstore/archive/refs/heads/main.zip",
}

// pickCatalogueSource asks the catalogue's URL whether it answers, and its
// mirror when it does not. It returns the URL to fetch and the answer, whose
// ContentLength says whether anything changed.
func pickCatalogueSource(url string, head func(string) (*http.Response, error)) (string, *http.Response, error) {
	res, err := head(url)
	if err == nil && res.StatusCode == http.StatusOK {
		return url, res, nil
	}

	if err == nil {
		err = fmt.Errorf("failed to get appstore size, status code: %d", res.StatusCode)
	}

	mirror, ok := catalogueMirrors[url]
	if !ok {
		return "", nil, err
	}

	logger.Warn("the catalogue does not answer; trying this distribution's copy of it",
		zap.String("url", url), zap.String("mirror", mirror), zap.Error(err))

	res, mirrorErr := head(mirror)
	if mirrorErr != nil {
		return "", nil, fmt.Errorf("%w (and the mirror: %v)", err, mirrorErr)
	}
	if res.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("%w (and the mirror answered %d)", err, res.StatusCode)
	}

	return mirror, res, nil
}
