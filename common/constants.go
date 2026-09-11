package common

const (
	AppManagementServiceName = "app-management"
	AppManagementVersion     = "0.4.19"

	AppsDirectoryName = "Apps"

	ComposeAppAuthorCasaOSTeam = "CasaOS Team"

	ComposeExtensionNameXCasaOS                = "x-casaos"
	ComposeExtensionPropertyNameStoreAppID     = "store_app_id"
	ComposeExtensionPropertyNameTitle          = "title"
	ComposeExtensionPropertyNameIsUncontrolled = "is_uncontrolled"

	ComposeYAMLFileName = "docker-compose.yml"

	ContainerLabelV1AppStoreID = "io.casaos.v1.app.store.id"

	DefaultCategoryFont = "grid"
	DefaultLanguage     = "en_us"
	DefaultPassword     = "casaos"
	DefaultPGID         = "1000"
	DefaultPUID         = "1000"
	DefaultUserName     = "admin"

	Localhost           = "127.0.0.1"
	MIMEApplicationYAML = "application/yaml"

	CategoryListFileName  = "category-list.json"
	RecommendListFileName = "recommend-list.json"
)

// Tags that name a STREAM rather than a version: the same name is republished as
// new images are built, so the name says nothing about which image is running and
// only the digest can tell whether it moved.
//
// This list decides three things, and they have to agree or the dashboard
// contradicts itself: whether an update rewrites the image in a compose file,
// whether a differing image counts as an update being available, and which
// version the upgradable list shows.
//
// It held "latest" alone, next to an upstream note saying more could be added.
// Everything else -- an app tracking `develop`, `nightly`, `edge` -- was read as a
// pinned version, so an update replaced the owner's choice of stream with
// whatever fixed tag the catalogue named.
//
// Being wrong in the inclusive direction is cheap: a tag listed here that is
// really a version means the compose file keeps the reference it has and the pull
// is what moves it, which is what a pull does anyway. Being wrong the other way
// silently un-picks somebody's choice.
var NeedCheckDigestTags = []string{
	"latest", "stable", "edge", "beta", "alpha",
	"main", "master", "develop", "dev",
	"nightly", "canary", "rolling", "unstable", "testing", "preview", "insiders",
}
