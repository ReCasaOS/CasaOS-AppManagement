package v2_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/model"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	v2 "github.com/ReCasaOS/CasaOS-AppManagement/route/v2"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/file"
	"go.uber.org/goleak"
	"gotest.tools/v3/assert"
)

func TestWebAppGridItemAdapter(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"), goleak.IgnoreAnyFunction("github.com/orca-zhang/ecache.init.0.func1")) // https://github.com/census-instrumentation/opencensus-go/issues/1191

	defer func() {
		// workaround due to https://github.com/patrickmn/go-cache/issues/166
		docker.Cache = nil
		runtime.GC()
	}()

	storeRoot := t.TempDir()

	appsPath := filepath.Join(storeRoot, common.AppsDirectoryName)
	err := file.MkDir(appsPath)
	assert.NilError(t, err)

	// build test catalog
	err = file.MkDir(filepath.Join(appsPath, "test1"))
	assert.NilError(t, err)

	composeFilePath := filepath.Join(appsPath, "test1", common.ComposeYAMLFileName)

	err = file.WriteToFullPath([]byte(common.SampleComposeAppYAML), composeFilePath, 0o644)
	assert.NilError(t, err)

	composeApp, err := service.LoadComposeAppFromConfigFile("test1", composeFilePath)
	assert.NilError(t, err)

	storeInfo, err := composeApp.StoreInfo(true)
	assert.NilError(t, err)

	composeAppWithStoreInfo := codegen.ComposeAppWithStoreInfo{
		Compose:   (*codegen.ComposeApp)(composeApp),
		StoreInfo: storeInfo,
		Status:    utils.Ptr("running"),
	}

	gridItem, err := v2.WebAppGridItemAdapterV2(&composeAppWithStoreInfo)
	assert.NilError(t, err)
	mainService, err := composeApp.MainService()
	assert.NilError(t, err)

	assert.Equal(t, *gridItem.Icon, storeInfo.Icon)
	assert.Equal(t, *gridItem.Image, mainService.Image)
	assert.Equal(t, gridItem.Hostname, storeInfo.Hostname)
	assert.Equal(t, *gridItem.Port, storeInfo.PortMap)
	assert.Equal(t, *gridItem.Index, storeInfo.Index)
	assert.Equal(t, *gridItem.Status, "running")
	assert.DeepEqual(t, *gridItem.Title, storeInfo.Title)
	assert.Equal(t, *gridItem.AuthorType, codegen.ByCasaos)
	assert.Equal(t, *gridItem.IsUncontrolled, false)
}

// An adopted container is offered operations that are not compose-aware, so the grid
// item has to say whether a project owns it. A project whose config file cannot be
// loaded is missing from the compose list and its containers arrive here one by one,
// looking exactly like a container nothing owns - the label they carry is the only
// thing that tells them apart.
func TestWebAppGridItemAdapterContainerReportsItsComposeProject(t *testing.T) {
	claimed, err := v2.WebAppGridItemAdapterContainer(&model.MyAppList{
		ID:             "3f1c",
		Name:           "immich_server_1",
		ComposeProject: "immich",
	})
	assert.NilError(t, err)
	assert.Equal(t, *claimed.ComposeProject, "immich")

	adopted, err := v2.WebAppGridItemAdapterContainer(&model.MyAppList{ID: "9a2b", Name: "plex"})
	assert.NilError(t, err)
	assert.Assert(t, adopted.ComposeProject == nil)
}

// The dashboard greys an app out from its status, and a stack written by hand has no
// `x-casaos` -- so no store info. The status was copied into the grid item inside the
// `store info is present` branch, which meant those apps arrived with none, and the
// dashboard drew the whole card grey while every container in the stack was up. The
// status is worked out from the containers, not from the catalogue; it does not belong
// behind that guard.
func TestWebAppGridItemCarriesStatusWithoutStoreInfo(t *testing.T) {
	storeRoot := t.TempDir()

	appsPath := filepath.Join(storeRoot, common.AppsDirectoryName)
	assert.NilError(t, file.MkDir(appsPath))
	assert.NilError(t, file.MkDir(filepath.Join(appsPath, "gluetun-stack")))

	composeFilePath := filepath.Join(appsPath, "gluetun-stack", common.ComposeYAMLFileName)
	assert.NilError(t, file.WriteToFullPath([]byte(common.SampleVanillaComposeAppYAML), composeFilePath, 0o644))

	composeApp, err := service.LoadComposeAppFromConfigFile("gluetun-stack", composeFilePath)
	assert.NilError(t, err)

	// the whole point of the fixture: no `x-casaos`, so no store info
	storeInfo, err := composeApp.StoreInfo(true)
	assert.Assert(t, storeInfo == nil)
	assert.ErrorIs(t, err, service.ErrComposeExtensionNameXCasaOSNotFound)

	gridItem, err := v2.WebAppGridItemAdapterV2(&codegen.ComposeAppWithStoreInfo{
		Compose:   (*codegen.ComposeApp)(composeApp),
		StoreInfo: nil,
		Status:    utils.Ptr("running"),
	})
	assert.NilError(t, err)

	assert.Assert(t, gridItem.Status != nil, "an app with no store info still has a status")
	assert.Equal(t, *gridItem.Status, "running")

	// and the rest of the item is the fallback, not a crash
	assert.Equal(t, *gridItem.Name, "gluetun-stack")
	assert.DeepEqual(t, *gridItem.Title, map[string]string{common.DefaultLanguage: "gluetun-stack"})
	assert.Assert(t, gridItem.Icon == nil)

	// a stopped stack still says so rather than falling back to a default
	stopped, err := v2.WebAppGridItemAdapterV2(&codegen.ComposeAppWithStoreInfo{
		Compose:   (*codegen.ComposeApp)(composeApp),
		StoreInfo: nil,
		Status:    utils.Ptr("exited"),
	})
	assert.NilError(t, err)
	assert.Equal(t, *stopped.Status, "exited")
}
