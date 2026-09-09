package service_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/codegen"
	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-AppManagement/pkg/config"
	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	"github.com/inkly/CasaOS-AppManagement/service"
	"github.com/inkly/CasaOS-Common/utils/file"
	"github.com/inkly/CasaOS-Common/utils/logger"
	"go.uber.org/goleak"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v3"
	"gotest.tools/v3/assert"
)

func TestAppStoreList(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction(topFunc1), goleak.IgnoreTopFunction(pollFunc1), goleak.IgnoreTopFunction(httpFunc1), goleak.IgnoreAnyFunction(ecacheClock)) // https://github.com/census-instrumentation/opencensus-go/issues/1191

	defer func() {
		// workaround due to https://github.com/patrickmn/go-cache/issues/166
		docker.Cache = nil
		runtime.GC()
	}()

	logger.LogInitConsoleOnly()

	file, err := os.CreateTemp("", "app-management.conf")
	assert.NilError(t, err)

	defer os.Remove(file.Name())

	config.InitSetup(file.Name(), "")
	config.AppInfo.AppStorePath = t.TempDir()

	appStoreManagement := service.NewAppStoreManagement()

	appStoreList := appStoreManagement.AppStoreList()
	assert.Equal(t, len(appStoreList), 0)

	registeredAppStoreList := []string{}
	appStoreManagement.OnAppStoreRegister(func(appStoreURL string) error {
		registeredAppStoreList = append(registeredAppStoreList, appStoreURL)
		return nil
	})

	unregisteredAppStoreList := []string{}
	appStoreManagement.OnAppStoreUnregister(func(appStoreURL string) error {
		unregisteredAppStoreList = append(unregisteredAppStoreList, appStoreURL)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = common.WithProperties(ctx, map[string]string{})

	expectAppStoreURL := strings.ToLower("https://github.com/IceWhaleTech/_appstore/archive/refs/heads/main.zip")

	ch := make(chan *codegen.AppStoreMetadata)

	err = appStoreManagement.RegisterAppStore(ctx, expectAppStoreURL, func(appStoreMetadata *codegen.AppStoreMetadata) {
		ch <- appStoreMetadata
	})
	assert.NilError(t, err)

	appStoreMetadata := <-ch
	assert.Equal(t, *appStoreMetadata.URL, expectAppStoreURL)
	assert.Assert(t, len(registeredAppStoreList) == 1)

	appStoreList = appStoreManagement.AppStoreList()
	assert.Equal(t, len(appStoreList), 1)

	actualAppStoreURL := *appStoreList[0].URL
	assert.Equal(t, actualAppStoreURL, expectAppStoreURL)

	err = appStoreManagement.UnregisterAppStore(0)
	assert.NilError(t, err)
	assert.Assert(t, len(unregisteredAppStoreList) == 1)

	assert.DeepEqual(t, registeredAppStoreList, unregisteredAppStoreList)
}

func TestIsUpgradable(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction(topFunc1), goleak.IgnoreTopFunction(pollFunc1), goleak.IgnoreTopFunction(httpFunc1), goleak.IgnoreAnyFunction(ecacheClock)) // https://github.com/census-instrumentation/opencensus-go/issues/1191

	defer func() {
		// workaround due to https://github.com/patrickmn/go-cache/issues/166
		docker.Cache = nil
		runtime.GC()
	}()

	logger.LogInitConsoleOnly()

	appStoreManagement := service.NewAppStoreManagement()

	// mock store compose app
	storeComposeApp, err := service.NewComposeAppFromYAML([]byte(common.SampleComposeAppYAML), true, false)
	assert.NilError(t, err)

	storeComposeApp.SetStoreAppID("test")

	storeMainApp, err := storeComposeApp.MainService()
	assert.NilError(t, err)
	storeMainAppImage, _ := docker.ExtractImageAndTag(storeMainApp.Image)

	storeComposeAppStoreInfo, err := storeComposeApp.StoreInfo(false)
	assert.NilError(t, err)

	// mock local compose app
	appsPath := t.TempDir()

	composeFilePath := filepath.Join(appsPath, common.ComposeYAMLFileName)

	buf, err := yaml.Marshal(storeComposeApp)
	assert.NilError(t, err)

	err = file.WriteToFullPath(buf, composeFilePath, 0o644)
	assert.NilError(t, err)

	localComposeApp, err := service.LoadComposeAppFromConfigFile(*storeComposeAppStoreInfo.StoreAppID, composeFilePath)
	assert.NilError(t, err)

	upgradable, err := appStoreManagement.IsUpdateAvailableWith(localComposeApp, storeComposeApp)
	assert.NilError(t, err)
	assert.Assert(t, !upgradable)

	storeMainService := storeComposeApp.Services[storeMainApp.Name]
	storeMainService.Image = storeMainAppImage + ":test"
	storeComposeApp.Services[storeMainApp.Name] = storeMainService

	upgradable, err = appStoreManagement.IsUpdateAvailableWith(localComposeApp, storeComposeApp)
	assert.NilError(t, err)
	assert.Assert(t, upgradable)

	// A store entry BEHIND what is installed is not an update. Answering true
	// here is what downgrades an app: the update writes the store's image over
	// the local one, whichever direction that moves the version.
	storeMainService.Image = storeMainAppImage + ":1.23.0"
	storeComposeApp.Services[storeMainApp.Name] = storeMainService

	upgradable, err = appStoreManagement.IsUpdateAvailableWith(localComposeApp, storeComposeApp)
	assert.NilError(t, err)
	assert.Assert(t, !upgradable)

	// one ahead of it still is
	storeMainService.Image = storeMainAppImage + ":1.23.2"
	storeComposeApp.Services[storeMainApp.Name] = storeMainService

	upgradable, err = appStoreManagement.IsUpdateAvailableWith(localComposeApp, storeComposeApp)
	assert.NilError(t, err)
	assert.Assert(t, upgradable)
}
