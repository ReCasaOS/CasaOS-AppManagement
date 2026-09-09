package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// An app that came from no store has no catalogue entry to take images from. Updating
// it means pulling the tags it already names and recreating from them, so the compose
// file an update writes has to be the one already on disk -- placeholders and all,
// never the runtime project with `.env` resolved into it.
func TestRefreshedComposeYAMLKeepsTheFileAsWritten(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	const compose = "name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy:1\n    environment:\n      SECRET: ${SECRET}\n" +
		"    ports:\n      - ${PORT}:80\n    volumes:\n      - ${DATA}:/data\nx-casaos:\n  title:\n    en_us: wg-easy\n  is_uncontrolled: true\n"
	assert.NilError(t, os.WriteFile(composeFile, []byte(compose), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=s3cr3t\nPORT=8080\nDATA=/srv/d\n"), 0o600))

	a, err := LoadComposeAppFromConfigFile("wg-easy", composeFile)
	assert.NilError(t, err)

	out, err := a.refreshedComposeYAML()
	assert.NilError(t, err)

	// the images it already names, untouched
	assert.Assert(t, strings.Contains(string(out), "image: ghcr.io/wg-easy/wg-easy:1"), string(out))

	// and not one resolved value baked in, which is what would kill `.env` on the
	// first update
	for _, leak := range []string{"s3cr3t", "8080", "/srv/d"} {
		assert.Assert(t, !strings.Contains(string(out), leak), "%s baked in\n%s", leak, out)
	}
	for _, want := range []string{"SECRET: ${SECRET}", "published: ${PORT}", "source: ${DATA}\n"} {
		assert.Assert(t, strings.Contains(string(out), want), "%s missing in\n%s", want, out)
	}

	// what it writes still loads and resolves the same way
	assert.NilError(t, os.WriteFile(composeFile, out, 0o600))
	running, err := LoadComposeAppFromConfigFile("wg-easy", composeFile)
	assert.NilError(t, err)
	service := running.Services["wg-easy"]
	assert.Equal(t, service.Image, "ghcr.io/wg-easy/wg-easy:1")
	assert.Equal(t, *service.Environment["SECRET"], "s3cr3t")
	assert.Equal(t, service.Ports[0].Published, "8080")
}

// An unreadable `.env` is an error rather than an empty keep-set, for the same reason
// it is in updatedComposeYAML: an empty keep-set bakes every value into the file.
func TestRefreshedComposeYAMLRefusesAnUnreadableEnv(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile,
		[]byte("name: a\nservices:\n  a:\n    image: acme/a:1\nx-casaos:\n  is_uncontrolled: true\n"), 0o600))
	a, err := LoadComposeAppFromConfigFile("a", composeFile)
	assert.NilError(t, err)

	// written after the load, because an unreadable .env stops the load too
	assert.NilError(t, os.WriteFile(a.EnvFile(), []byte("KEY VALUE\n"), 0o600))

	_, err = a.refreshedComposeYAML()
	assert.ErrorContains(t, err, "line 1")
}

// The choice of what an update writes, as a pure function of what the catalogue
// holds: nil means it holds nothing for this app.
func TestComposeYAMLForUpdateKeepsAnImportedAppsOwnImages(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)

	// What an imported app actually looks like on disk. Install stamps store_app_id
	// with the app's own name onto anything carrying an x-casaos map, and
	// IsNewComposeUncontrolled writes is_uncontrolled FALSE when it finds no store
	// entry. What makes it imported is that no catalogue holds that id, which is why
	// keying this on the flag reached none of these apps.
	assert.NilError(t, os.WriteFile(composeFile, []byte(
		"name: a\nservices:\n  a:\n    image: acme/a:1\n"+
			"x-casaos:\n  main: a\n  store_app_id: a\n  is_uncontrolled: false\n"), 0o600))

	a, err := LoadComposeAppFromConfigFile("a", composeFile)
	assert.NilError(t, err)
	storeInfo, err := a.StoreInfo(false)
	assert.NilError(t, err)
	assert.Equal(t, *storeInfo.StoreAppID, "a")
	assert.Equal(t, *storeInfo.IsUncontrolled, false)

	out, err := a.composeYAMLForUpdate(nil)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(out), "image: acme/a:1"), string(out))
}

func TestComposeYAMLForUpdateTakesTheCatalogueImagesWhenThereIsAnEntry(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile, []byte(
		"name: a\nservices:\n  a:\n    image: acme/a:1\nx-casaos:\n  main: a\n  store_app_id: a\n"), 0o600))

	a, err := LoadComposeAppFromConfigFile("a", composeFile)
	assert.NilError(t, err)

	store, err := NewComposeAppFromYAML([]byte(
		"name: a\nservices:\n  a:\n    image: acme/a:2\nx-casaos:\n  main: a\n"), true, true)
	assert.NilError(t, err)

	out, err := a.composeYAMLForUpdate(store)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(out), "image: acme/a:2"), string(out))
}

// The dashboard badges an app from what the image check found, so the update button
// has to agree with the badge. Before this, an imported app was never updatable at
// all, and a store app whose tag had not moved read as up to date even when its image
// had been republished under that same tag.
func TestIsUpdateAvailableAgreesWithTheImageCheck(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)

	// an imported app, as it is actually written: a store_app_id no catalogue holds
	assert.NilError(t, os.WriteFile(composeFile, []byte(
		"name: imported\nservices:\n  a:\n    image: acme/a:1\n"+
			"x-casaos:\n  main: a\n  store_app_id: imported\n  is_uncontrolled: false\n"), 0o600))

	a, err := LoadComposeAppFromConfigFile("imported", composeFile)
	assert.NilError(t, err)

	appStore := NewAppStoreManagement()

	imageUpdates.byApp = map[string]bool{}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	// The answer is cached for an hour, so each case forgets the previous one --
	// which is exactly what the update route does after checking, and why it has to.
	answer := func() bool {
		appStore.ForgetUpgradable("imported")
		return appStore.IsUpdateAvailable(a)
	}

	// nobody has checked: nothing to offer
	assert.Assert(t, !answer())

	imageUpdates.byApp = map[string]bool{"imported": false}
	assert.Assert(t, !answer())

	imageUpdates.byApp = map[string]bool{"imported": true}
	assert.Assert(t, answer())
}

// A moved image must not be allowed to smuggle a downgrade back in. For a store app
// the update writes the STORE's compose, so answering "yes, your image moved" when
// the catalogue sits on an older tag would install that older tag -- exactly the
// rollback the tag comparison exists to prevent.
func TestImageCheckDoesNotReopenTheDowngrade(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile,
		[]byte("name: app\nservices:\n  a:\n    image: acme/a:2.0\nx-casaos:\n  main: a\n  is_uncontrolled: false\n"), 0o600))

	local, err := LoadComposeAppFromConfigFile("app", composeFile)
	assert.NilError(t, err)

	appStore := NewAppStoreManagement()

	imageUpdates.byApp = map[string]bool{"app": true}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	behind, err := NewComposeAppFromYAML(
		[]byte("name: app\nservices:\n  a:\n    image: acme/a:1.8\nx-casaos:\n  main: a\n"), true, true)
	assert.NilError(t, err)
	updatable, err := appStore.IsUpdateAvailableWith(local, behind)
	assert.NilError(t, err)
	assert.Assert(t, !updatable, "a catalogue behind the installed app is not an update, moved image or not")

	// the same tag, though, means the update is a re-pull of it, and there the image
	// check is the only thing that can say whether that would fetch anything
	same, err := NewComposeAppFromYAML(
		[]byte("name: app\nservices:\n  a:\n    image: acme/a:2.0\nx-casaos:\n  main: a\n"), true, true)
	assert.NilError(t, err)
	updatable, err = appStore.IsUpdateAvailableWith(local, same)
	assert.NilError(t, err)
	assert.Assert(t, updatable)

	imageUpdates.byApp = map[string]bool{"app": false}
	updatable, err = appStore.IsUpdateAvailableWith(local, same)
	assert.NilError(t, err)
	assert.Assert(t, !updatable)
}

// The tag comparison looks at the MAIN service only, but an update rewrites every
// service's image to the catalogue's. So "the catalogue is on my tag, and one of my
// images has moved" is not enough: the sidecar the catalogue still pins lower would be
// written back over a newer one, and an older sidecar that starts and migrates in
// place takes the data with it.
func TestSameMainTagDoesNotDowngradeASidecar(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile, []byte(
		"name: app\nservices:\n  app:\n    image: acme/app:2.0\n  db:\n    image: postgres:16\n"+
			"x-casaos:\n  main: app\n"), 0o600))

	local, err := LoadComposeAppFromConfigFile("app", composeFile)
	assert.NilError(t, err)

	appStore := NewAppStoreManagement()

	// the registry republished one of the images, so the app reads as updatable
	imageUpdates.byApp = map[string]bool{"app": true}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	// same main tag, but the catalogue still pins the sidecar lower
	lagging, err := NewComposeAppFromYAML([]byte(
		"name: app\nservices:\n  app:\n    image: acme/app:2.0\n  db:\n    image: postgres:15\n"+
			"x-casaos:\n  main: app\n"), true, true)
	assert.NilError(t, err)

	updatable, err := appStore.IsUpdateAvailableWith(local, lagging)
	assert.NilError(t, err)
	assert.Assert(t, !updatable, "applying this would write postgres:15 over postgres:16")

	// the catalogue naming the same images throughout is a pure re-pull, and there the
	// image check is the only thing that can say whether it would fetch anything
	matching, err := NewComposeAppFromYAML([]byte(
		"name: app\nservices:\n  app:\n    image: acme/app:2.0\n  db:\n    image: postgres:16\n"+
			"x-casaos:\n  main: app\n"), true, true)
	assert.NilError(t, err)

	updatable, err = appStore.IsUpdateAvailableWith(local, matching)
	assert.NilError(t, err)
	assert.Assert(t, updatable)
}

// An image pinned by digest has no tag to order, and the extractor now says so with
// an empty tag rather than handing back the digest's hex. Without a branch of its own
// every digest-pinned app would compare equal to every other and never update again.
func TestDigestPinnedAppComparesByReference(t *testing.T) {
	logger.LogInitConsoleOnly()

	const a = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	const b = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile, []byte(
		"name: app\nservices:\n  a:\n    image: acme/app@"+a+"\nx-casaos:\n  main: a\n"), 0o600))

	local, err := LoadComposeAppFromConfigFile("app", composeFile)
	assert.NilError(t, err)

	appStore := NewAppStoreManagement()

	// stated rather than inherited from whatever ran before: the identical-digest
	// case below returns through the sameImages branch, which reads this map
	imageUpdates.byApp = map[string]bool{}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	moved, err := NewComposeAppFromYAML([]byte(
		"name: app\nservices:\n  a:\n    image: acme/app@"+b+"\nx-casaos:\n  main: a\n"), true, true)
	assert.NilError(t, err)
	updatable, err := appStore.IsUpdateAvailableWith(local, moved)
	assert.NilError(t, err)
	assert.Assert(t, updatable)

	same, err := NewComposeAppFromYAML([]byte(
		"name: app\nservices:\n  a:\n    image: acme/app@"+a+"\nx-casaos:\n  main: a\n"), true, true)
	assert.NilError(t, err)
	updatable, err = appStore.IsUpdateAvailableWith(local, same)
	assert.NilError(t, err)
	assert.Assert(t, !updatable)
}
