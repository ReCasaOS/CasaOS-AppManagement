package v2_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/codegen"
	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-AppManagement/model"
	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	v2 "github.com/inkly/CasaOS-AppManagement/route/v2"
	"github.com/inkly/CasaOS-AppManagement/service"
	"github.com/inkly/CasaOS-Common/utils"
	"github.com/inkly/CasaOS-Common/utils/file"
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
