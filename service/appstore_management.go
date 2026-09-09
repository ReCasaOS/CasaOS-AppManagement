package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/bluele/gcache"
	"github.com/inkly/CasaOS-AppManagement/codegen"
	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-AppManagement/pkg/config"
	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	pkg_utils "github.com/inkly/CasaOS-AppManagement/pkg/utils"
	"github.com/inkly/CasaOS-Common/utils"
	"github.com/inkly/CasaOS-Common/utils/file"
	"github.com/inkly/CasaOS-Common/utils/logger"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var ErrAppStoreSourceExists = fmt.Errorf("appstore source already exists")

type AppStoreManagement struct {
	isAppUpgradable      gcache.Cache
	defaultAppStore      AppStore
	isAppUpgrading       sync.Map
	onAppStoreRegister   []func(string) error
	onAppStoreUnregister []func(string) error
}

func (a *AppStoreManagement) AppStoreList() []codegen.AppStoreMetadata {
	return lo.Map(config.ServerInfo.AppStoreList, func(appStoreURL string, id int) codegen.AppStoreMetadata {
		appStore, err := AppStoreByURL(appStoreURL)
		if err != nil {
			logger.Error("failed to construct appstore", zap.Error(err), zap.String("appstoreURL", appStoreURL))
			return codegen.AppStoreMetadata{}
		}

		workDir, err := appStore.WorkDir()
		if err != nil {
			logger.Error("failed to get appstore workdir", zap.Error(err), zap.String("appstoreURL", appStoreURL))
			return codegen.AppStoreMetadata{}
		}

		storeRoot, err := StoreRoot(workDir)
		if err != nil {
			logger.Error("failed to get appstore storeRoot", zap.Error(err), zap.String("appstoreURL", appStoreURL))
			storeRoot = "internal error - store root not found"
		}

		return codegen.AppStoreMetadata{
			ID:        &id,
			URL:       &appStoreURL,
			StoreRoot: &storeRoot,
		}
	})
}

func (a *AppStoreManagement) OnAppStoreRegister(fn func(string) error) {
	a.onAppStoreRegister = append(a.onAppStoreRegister, fn)
}

func (a *AppStoreManagement) OnAppStoreUnregister(fn func(string) error) {
	a.onAppStoreUnregister = append(a.onAppStoreUnregister, fn)
}

func (a *AppStoreManagement) ChangeGlobal(key string, value string) error {
	config.Global[key] = value

	go func() {
		if err := config.SaveGlobal(); err != nil {
			logger.Error("failed to save global env", zap.Error(err), zap.String("key", key), zap.String("value", value))
			return
		}
	}()

	return nil
}

func (a *AppStoreManagement) DeleteGlobal(key string) error {
	for k := range config.Global {
		if k == key {
			delete(config.Global, k)
		}
	}

	go func() {
		if err := config.SaveGlobal(); err != nil {
			logger.Error("failed to delete global env", zap.Error(err), zap.String("key", key))
			return
		}
	}()

	return nil
}

func (a *AppStoreManagement) RegisterAppStore(ctx context.Context, appstoreURL string, callbacks ...func(*codegen.AppStoreMetadata)) error {
	// check if appstore already exists
	for _, url := range config.ServerInfo.AppStoreList {
		if strings.EqualFold(url, appstoreURL) {
			return ErrAppStoreSourceExists
		}
	}

	appstore, err := AppStoreByURL(appstoreURL)
	if err != nil {
		return err
	}

	go func() {
		go PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterBegin, nil)

		defer PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterEnd, nil)

		var err error

		defer func() {
			if err == nil {
				return
			}

			PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
		}()

		if err = appstore.UpdateCatalog(); err != nil {
			logger.Error("failed to update appstore catalog", zap.Error(err), zap.String("appstoreURL", appstoreURL))

			return
		}

		// if everything is good, add to the list
		config.ServerInfo.AppStoreList = append(config.ServerInfo.AppStoreList, appstoreURL)

		if err = config.SaveSetup(); err != nil {
			logger.Error("failed to save appstore list", zap.Error(err), zap.String("appstoreURL", appstoreURL))
			return
		}

		for _, fn := range a.onAppStoreRegister {
			if err := fn(appstoreURL); err != nil {
				logger.Error("failed to run onAppStoreRegister", zap.Error(err), zap.String("appstoreURL", appstoreURL))
			}
		}

		appStoreMetadata := &codegen.AppStoreMetadata{
			ID:  utils.Ptr(len(config.ServerInfo.AppStoreList) - 1),
			URL: &appstoreURL,
		}

		for _, callback := range callbacks {
			callback(appStoreMetadata)
		}
	}()

	return nil
}

// TODO: refactor the function and above function
func (a *AppStoreManagement) RegisterAppStoreSync(ctx context.Context, appstoreURL string, callbacks ...func(*codegen.AppStoreMetadata)) error {
	// check if appstore already exists
	for _, url := range config.ServerInfo.AppStoreList {
		if strings.EqualFold(url, appstoreURL) {
			return ErrAppStoreSourceExists
		}
	}

	appstore, err := AppStoreByURL(appstoreURL)
	if err != nil {
		return err
	}

	go PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterBegin, nil)

	defer PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterEnd, nil)

	defer func() {
		if err == nil {
			return
		}

		PublishEventWrapper(ctx, common.EventTypeAppStoreRegisterError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})
	}()

	if err = appstore.UpdateCatalog(); err != nil {
		logger.Error("failed to update appstore catalog", zap.Error(err), zap.String("appstoreURL", appstoreURL))

		return err
	}

	// if everything is good, add to the list
	config.ServerInfo.AppStoreList = append(config.ServerInfo.AppStoreList, appstoreURL)

	if err = config.SaveSetup(); err != nil {
		logger.Error("failed to save appstore list", zap.Error(err), zap.String("appstoreURL", appstoreURL))
		return err
	}

	for _, fn := range a.onAppStoreRegister {
		if err := fn(appstoreURL); err != nil {
			logger.Error("failed to run onAppStoreRegister", zap.Error(err), zap.String("appstoreURL", appstoreURL))
		}
	}

	appStoreMetadata := &codegen.AppStoreMetadata{
		ID:  utils.Ptr(len(config.ServerInfo.AppStoreList) - 1),
		URL: &appstoreURL,
	}

	for _, callback := range callbacks {
		callback(appStoreMetadata)
	}

	return nil
}

func (a *AppStoreManagement) UnregisterAppStore(appStoreID uint) error {
	if appStoreID >= uint(len(config.ServerInfo.AppStoreList)) {
		return fmt.Errorf("appstore id %d out of range", appStoreID)
	}

	appStoreURL := config.ServerInfo.AppStoreList[appStoreID]

	// remove appstore from list
	{
		config.ServerInfo.AppStoreList = append(config.ServerInfo.AppStoreList[:appStoreID], config.ServerInfo.AppStoreList[appStoreID+1:]...)

		if err := config.SaveSetup(); err != nil {
			return err
		}
	}

	// remove appstore workdir
	{
		appStore, err := AppStoreByURL(appStoreURL)
		if err != nil {
			return err
		}

		workdir, err := appStore.WorkDir()
		if err != nil {
			logger.Error("error while getting appstore workdir", zap.Error(err), zap.String("url", appStoreURL))
		}

		if len(workdir) != 0 {
			if err := file.RMDir(workdir); err != nil {
				logger.Error("error while removing appstore workdir", zap.Error(err), zap.String("workdir", workdir))
			}
		}
	}

	for _, fn := range a.onAppStoreUnregister {
		if err := fn(appStoreURL); err != nil {
			return err
		}
	}
	return nil
}

func (a *AppStoreManagement) AppStoreMap() (map[string]AppStore, error) {
	appStoreMap := lo.SliceToMap(config.ServerInfo.AppStoreList, func(appStoreURL string) (string, AppStore) {
		appStore, err := AppStoreByURL(appStoreURL)
		if err != nil {
			return "", nil
		}
		return appStoreURL, appStore
	})

	delete(appStoreMap, "")

	return appStoreMap, nil
}

// AppStore interface
func (a *AppStoreManagement) CategoryMap() (map[string]codegen.CategoryInfo, error) {
	appStoreMap, err := a.AppStoreMap()
	if err != nil {
		return nil, err
	}

	allFailed := true

	categoryMap := map[string]codegen.CategoryInfo{}
	for _, appStore := range appStoreMap {
		c, err := appStore.CategoryMap()
		if err != nil {
			logger.Error("error while loading category map", zap.Error(err))
			continue
		}

		allFailed = false

		for name, category := range c {
			categoryMap[name] = category
		}
	}

	if allFailed {
		logger.Info("all appstores failed to load category map, using default")

		categoryMap, err = a.defaultAppStore.CategoryMap()
		if err != nil {
			return nil, err
		}
	}

	for name, category := range categoryMap {
		category.Count = utils.Ptr(0)
		categoryMap[name] = category
	}

	catalog, err := a.Catalog()
	if err != nil {
		return nil, err
	}

	for _, app := range catalog {
		// count what the list endpoint actually returns, which is architecture-filtered
		if !app.SupportsArchitecture(pkg_utils.GetCPUArch()) {
			continue
		}

		storeInfo, err := app.StoreInfo(false)
		if err != nil {
			continue
		}

		category, ok := categoryMap[storeInfo.Category]
		if !ok {
			continue
		}

		category.Count = lo.ToPtr(*category.Count + 1)

		categoryMap[storeInfo.Category] = category
	}

	return categoryMap, nil
}

func (a *AppStoreManagement) Recommend() ([]string, error) {
	appStoreMap, err := a.AppStoreMap()
	if err != nil {
		logger.Error("error while loading appstore map", zap.Error(err))
		return nil, err
	}

	allFailed := true

	recommend := []string{}
	for _, appStore := range appStoreMap {
		r, err := appStore.Recommend()
		if err != nil {
			logger.Error("error while getting appstore recommend", zap.Error(err))
			continue
		}

		allFailed = false
		recommend = lo.Union(recommend, r)
	}

	if !allFailed {
		return recommend, nil
	}

	logger.Info("No appstore registered")
	if a.defaultAppStore == nil {
		logger.Info("WARNING - no default appstore")
		return nil, nil
	}

	logger.Info("Using default appstore")
	recommend, err = a.defaultAppStore.Recommend()
	if err != nil {
		logger.Error("error while getting default appstore recommend list", zap.Error(err))
		return nil, err
	}

	return recommend, nil
}

func (a *AppStoreManagement) Catalog() (map[string]*ComposeApp, error) {
	catalog := map[string]*ComposeApp{}

	appStoreMap, err := a.AppStoreMap()
	if err != nil {
		return nil, err
	}

	allFailed := true

	for _, appStore := range appStoreMap {

		c, err := appStore.Catalog()
		if err != nil {
			logger.Error("error while getting appstore catalog", zap.Error(err))
			continue
		}

		allFailed = false
		for storeAppID, composeApp := range c {
			catalog[storeAppID] = composeApp
		}
	}

	if !allFailed {
		return catalog, nil
	}

	logger.Info("No appstore registered")
	if a.defaultAppStore == nil {
		logger.Info("WARNING - no default appstore")
		return map[string]*ComposeApp{}, nil
	}

	logger.Info("Using default appstore")
	catalog, err = a.defaultAppStore.Catalog()
	if err != nil {
		return map[string]*ComposeApp{}, err
	}

	return catalog, nil
}

func (a *AppStoreManagement) UpdateCatalog() error {
	// reload config.
	// the appstore may be change in runtime.
	config.ReloadConfig()

	appStoreMap, err := a.AppStoreMap()
	if err != nil {
		return err
	}

	for url, appStore := range appStoreMap {
		if err := appStore.UpdateCatalog(); err != nil {
			logger.Error("error while updating catalog for app store", zap.Error(err), zap.String("url", url))
		}
	}

	// clean cache
	a.isAppUpgradable.Purge()

	return nil
}

func (a *AppStoreManagement) ComposeApp(id string) (*ComposeApp, error) {
	appStoreMap, err := a.AppStoreMap()
	if err != nil {
		return nil, err
	}

	for _, appStore := range appStoreMap {
		composeApp, appErr := appStore.ComposeApp(id)
		if appErr != nil {
			logger.Error("error while getting appstore compose app", zap.Error(appErr))
			continue
		}

		if composeApp != nil {
			return composeApp, nil
		}
	}

	logger.Info("app not found in any appstore", zap.String("id", id))

	if a.defaultAppStore == nil {
		logger.Info("WARNING - no default appstore")
		return nil, nil
	}

	logger.Info("Using default appstore")

	composeApp, err := a.defaultAppStore.ComposeApp(id)
	if err != nil {
		return nil, err
	}

	return composeApp, nil
}

func (a *AppStoreManagement) WorkDir() (string, error) {
	panic("not implemented and will never be implemented - this is a virtual appstore")
}

func (a *AppStoreManagement) IsUpdateAvailable(composeApp *ComposeApp) bool {
	storeID := composeApp.Name
	if value, err := a.isAppUpgradable.Get(storeID); err == nil {
		switch value := value.(type) {
		case bool:
			return value
		default:
			logger.Error("invalid type in cache", zap.String("storeID", storeID), zap.Any("value", value))
			return false
		}
	}

	isUpdate, err := a.isUpdateAvailable(composeApp)
	if err != nil {
		logger.Error("failed to check if update is available", zap.Error(err))
		return false
	}
	_ = a.isAppUpgradable.Set(storeID, isUpdate)
	return isUpdate
}

func (a *AppStoreManagement) isUpdateAvailable(composeApp *ComposeApp) (bool, error) {
	// handle no tag logic and for easy to test
	storeInfo, err := composeApp.StoreInfo(false)
	if err != nil {
		logger.Error("failed to get store info of compose app, thus no update available", zap.Error(err))
		return false, nil
	}

	if storeInfo == nil || storeInfo.StoreAppID == nil || *storeInfo.StoreAppID == "" {
		return false, err
	}

	storeComposeApp, err := a.ComposeApp(*storeInfo.StoreAppID)
	if err != nil {
		logger.Error("failed to get store compose app, thus no update available", zap.Error(err))
		return false, err
	}

	// No catalogue entry, so there is nothing to compare a tag against and the update
	// would be a re-pull of the tags this app already names. The image check is the
	// whole answer. This used to be a flat no, which is why an imported app could
	// never update.
	if storeComposeApp == nil {
		return imageUpdatable(composeApp.Name), nil
	}

	return a.IsUpdateAvailableWith(composeApp, storeComposeApp)
}

// the patch is have no choice
// the digest compare is not work for these images
// I don't know why, but I have to do this
// I will remove the patch after I rewrite the digest compare
var NoUpdateBlacklist = []string{
	"johnguan/stable-diffusion-webui:latest",
}

// IsUpdateAvailableWith decides an update SERVICE BY SERVICE, because that is what an
// update writes: every service the catalogue names takes the catalogue's image.
//
// It is on offer when no service would go backwards and something would actually
// change -- the catalogue names an image this app does not run, or the registries say
// an image it does run has moved. Reading the MAIN service's tag alone made a
// catalogue that bumps only a sidecar invisible, which is what froze the multi-service
// stacks; and demanding every image be identical before believing the registries threw
// their answer away over any difference at all, including a service the owner added
// by hand.
func (a *AppStoreManagement) IsUpdateAvailableWith(composeApp *ComposeApp, storeComposeApp *ComposeApp) (bool, error) {
	changed := false

	for name, service := range composeApp.Services {
		// The digest comparison is wrong for these images and it is the only thing
		// that could offer an update for one, so a blacklisted image anywhere in the
		// app is a flat no.
		if lo.Contains(NoUpdateBlacklist, service.Image) {
			return false, nil
		}

		storeService, ok := storeComposeApp.Services[name]
		if !ok {
			// The owner's own service -- a VPN sidecar wired in by hand, a companion
			// container. The catalogue says nothing about it and an update leaves it
			// alone, so it can neither offer an update nor block one.
			continue
		}

		if storeService.Image == service.Image {
			continue
		}

		if goesBackwards(service.Image, storeService.Image) {
			// Applying this catalogue would write an older image over a newer one for
			// this service. An update is all services at once, so there is no partial
			// one to offer -- and an older sidecar that starts and migrates in place
			// takes the data with it.
			return false, nil
		}

		changed = true
	}

	if changed {
		return true, nil
	}

	// The catalogue names every image this app already runs, so an update would
	// re-pull them rather than move the app anywhere. Whether that fetches anything is
	// a question only a registry can answer, and the image check asked it -- of the
	// containers this app is RUNNING, which no comparison against the file on disk can
	// see. It is also the whole answer for a tag like `latest`, republished under the
	// same name where comparing tags sees nothing move.
	return imageUpdatable(composeApp.Name), nil
}

// goesBackwards reports whether writing storeImage over localImage would move that
// service to an older version -- the one thing an update must never be offered for.
func goesBackwards(localImage, storeImage string) bool {
	_, localTag := docker.ExtractImageAndTag(localImage)
	_, storeTag := docker.ExtractImageAndTag(storeImage)

	// A reference pinned by digest has no tag to order, and the same tag on another
	// reference is a move between repositories. Neither is a version going backwards;
	// both are the catalogue's statement about what this app should run.
	if localTag == "" || storeTag == "" || localTag == storeTag {
		return false
	}

	return !isNewerTag(storeTag, localTag)
}

// isNewerTag answers whether moving from current to candidate is an upgrade.
//
// An update overwrites the installed image with the store's, in whichever
// direction that moves the version, so a store entry that has fallen behind what
// is installed must not be offered as an update -- answering yes there is what
// downgraded apps.
//
// Tags that are not versions cannot be ordered: latest, stable, a codename, a
// build id. For those any difference still counts as an update, which is the
// behaviour this had for every tag, and is the store's own statement about what
// the app should run.
func isNewerTag(candidate, current string) bool {
	if candidate == current {
		return false
	}

	candidateVersion, candidateErr := semver.NewVersion(candidate)
	currentVersion, currentErr := semver.NewVersion(current)
	if candidateErr != nil || currentErr != nil {
		return true
	}

	// Same upstream version, different build suffix. SemVer orders prerelease
	// identifiers letter by letter, so `ls99` sorts above `ls124` -- and
	// linuxserver.io, whose images are most of what a home server runs, crosses that
	// boundary at every hundredth build. Compare the digit runs as numbers.
	if candidateVersion.Prerelease() != "" && currentVersion.Prerelease() != "" &&
		candidateVersion.Major() == currentVersion.Major() &&
		candidateVersion.Minor() == currentVersion.Minor() &&
		candidateVersion.Patch() == currentVersion.Patch() {
		return naturalCompare(candidateVersion.Prerelease(), currentVersion.Prerelease()) > 0
	}

	return candidateVersion.GreaterThan(currentVersion)
}

// naturalCompare orders two strings with runs of digits compared as numbers rather
// than character by character, so `ls2` sorts below `ls10`.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			adigits, brest := digitRun(a), digitRun(b)
			if cmp := compareNumbers(adigits, brest); cmp != 0 {
				return cmp
			}
			a, b = a[len(adigits):], b[len(brest):]

			continue
		}

		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}

			return 1
		}
		a, b = a[1:], b[1:]
	}

	switch {
	case len(a) == len(b):
		return 0
	case len(a) < len(b):
		return -1
	default:
		return 1
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) string {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}

	return s[:i]
}

// compareNumbers compares two runs of digits by value, without converting them: a
// build number long enough to overflow is still a build number.
func compareNumbers(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}

		return 1
	}

	return strings.Compare(a, b)
}

func (a *AppStoreManagement) IsUpdating(appID string) bool {
	_, ok := a.isAppUpgrading.Load(appID)
	return ok
}

func (a *AppStoreManagement) StartUpgrade(appID string) {
	a.isAppUpgrading.Store(appID, struct{}{})
}

// ForgetUpgradable drops one app's cached answer so the next question is asked
// afresh. An hour of cache is right for a list rendered in the background and wrong
// immediately after someone pressed a button with the word "check" on it.
func (a *AppStoreManagement) ForgetUpgradable(appID string) {
	a.isAppUpgradable.Remove(appID)
}

func (a *AppStoreManagement) FinishUpgrade(appID string) {
	a.isAppUpgrading.Delete(appID)
	a.isAppUpgradable.Remove(appID)
}

func NewAppStoreManagement() *AppStoreManagement {
	defaultAppStore, err := NewDefaultAppStore()
	if err != nil {
		fmt.Printf("error while loading default appstore: %s\n", err.Error())
	}

	appStoreManagement := &AppStoreManagement{
		defaultAppStore: defaultAppStore,
		isAppUpgradable: gcache.New(100).LRU().Expiration(1 * time.Hour).Build(),
		isAppUpgrading:  sync.Map{},
	}

	return appStoreManagement
}
