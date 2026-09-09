package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/inkly/CasaOS-AppManagement/service/v1"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	composeCmd "github.com/docker/compose/v2/cmd/compose"
	"github.com/inkly/CasaOS-AppManagement/codegen"
	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-AppManagement/pkg/config"
	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	"github.com/inkly/CasaOS-Common/external"
	"github.com/inkly/CasaOS-Common/utils"
	"github.com/inkly/CasaOS-Common/utils/file"
	"github.com/inkly/CasaOS-Common/utils/logger"
	"github.com/inkly/CasaOS-Common/utils/port"
	"github.com/inkly/CasaOS-Common/utils/random"

	"github.com/docker/compose/v2/cmd/formatter"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/go-resty/resty/v2"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type ComposeApp codegen.ComposeApp

func (a *ComposeApp) StoreInfo(includeApps bool) (*codegen.ComposeAppStoreInfo, error) {
	ex, ok := a.Extensions[common.ComposeExtensionNameXCasaOS]
	if !ok {
		return nil, ErrComposeExtensionNameXCasaOSNotFound
	}

	var storeInfo codegen.ComposeAppStoreInfo
	if err := loader.Transform(ex, &storeInfo); err != nil {
		logger.Error("Transform store info fail", zap.Error(err))
		return nil, err
	}

	// TODO refactor this with ComposeAppWithStoreInfo
	isUncontrolled, ok := a.Extensions[common.ComposeExtensionNameXCasaOS].(map[string]interface{})[common.ComposeExtensionPropertyNameIsUncontrolled].(bool)
	if ok {
		storeInfo.IsUncontrolled = &isUncontrolled
	}

	// locate main app
	if storeInfo.Main == nil || *storeInfo.Main == "" {
		// if main app is not specified, use the first app
		for _, name := range sortedServiceNames(a.Services) {
			app := a.App(name)
			storeInfo.Main = &app.Name
			break
		}
	}

	if storeInfo.Scheme == nil || *storeInfo.Scheme == "" {
		storeInfo.Scheme = lo.ToPtr(codegen.Http)
	}

	if includeApps {
		apps := map[string]codegen.AppStoreInfo{}

		for _, name := range sortedServiceNames(a.Services) {
			app := a.App(name)
			appStoreInfo, err := app.StoreInfo()
			if err != nil {
				if err == ErrComposeExtensionNameXCasaOSNotFound {
					logger.Info("App does not have x-casaos extension - skipping", zap.String("app", app.Name))
					continue
				}

				return nil, err
			}
			apps[app.Name] = appStoreInfo
		}

		storeInfo.Apps = &apps
	}

	return &storeInfo, nil
}

func (a *ComposeApp) AuthorType() codegen.StoreAppAuthorType {
	storeInfo, err := a.StoreInfo(false)
	if err != nil {
		return codegen.Unknown
	}

	if strings.EqualFold(storeInfo.Author, storeInfo.Developer) {
		return codegen.Official
	}
	if strings.EqualFold(storeInfo.Author, common.ComposeAppAuthorCasaOSTeam) {
		return codegen.ByCasaos
	}

	return codegen.Community
}

// SupportsArchitecture reports whether the compose app runs on arch (a runtime.GOARCH value:
// amd64, arm64, arm). A missing or empty `architectures` list means every architecture,
// matching the UI (AppPanel.vue `unuseable`).
func (a *ComposeApp) SupportsArchitecture(arch string) bool {
	storeInfo, err := a.StoreInfo(false)
	if err != nil {
		return false
	}

	if storeInfo.Architectures == nil || len(*storeInfo.Architectures) == 0 {
		return true
	}

	return lo.Contains(*storeInfo.Architectures, arch)
}

func (a *ComposeApp) SetStoreAppID(storeAppID string) (string, bool) {
	// set store_app_id (by convention is the same as app name at install time if it does not exist)
	extension, ok := a.Extensions[common.ComposeExtensionNameXCasaOS]
	if !ok {
		logger.Info("compose app does not have x-casaos extension - might not be a compose app for CasaOS", zap.String("app", a.Name))
		return "", false
	}

	composeAppStoreInfo, ok := extension.(map[string]interface{})
	if !ok {
		logger.Info("compose app does not have valid x-casaos extension - might not be a compose app for CasaOS", zap.String("app", a.Name))
		return "", false
	}

	value, ok := composeAppStoreInfo[common.ComposeExtensionPropertyNameStoreAppID]
	if ok {
		currentStoreAppID, ok := value.(string)
		if ok {
			logger.Info("compose app already has store_app_id", zap.String("app", a.Name), zap.String("storeAppID", currentStoreAppID))
			return currentStoreAppID, true
		}
	}

	composeAppStoreInfo[common.ComposeExtensionPropertyNameStoreAppID] = storeAppID
	return storeAppID, true
}

func (a *ComposeApp) SetTitle(title, lang string) {
	if a.Extensions == nil {
		a.Extensions = make(map[string]interface{})
	}

	extension, ok := a.Extensions[common.ComposeExtensionNameXCasaOS]
	if !ok {
		extension = map[string]interface{}{}
		a.Extensions[common.ComposeExtensionNameXCasaOS] = extension
	}

	composeAppStoreInfo, ok := extension.(map[string]interface{})
	if !ok {
		logger.Info("compose app does not have valid x-casaos extension - might not be a compose app for CasaOS", zap.String("app", a.Name))
		return
	}

	if _, ok := composeAppStoreInfo[common.ComposeExtensionPropertyNameTitle]; !ok {
		composeAppStoreInfo[common.ComposeExtensionPropertyNameTitle] = map[string]string{}
	}

	titleMap, ok := composeAppStoreInfo[common.ComposeExtensionPropertyNameTitle].(map[string]string)
	if !ok {
		logger.Info("compose app does not have valid title map in its x-casaos extension - might not be a compose app for CasaOS", zap.String("app", a.Name))
		return
	}

	if _, ok := titleMap[lang]; !ok {
		titleMap[lang] = title
	}
}

func (a *ComposeApp) Update(ctx context.Context) error {
	if len(a.ComposeFiles) <= 0 {
		return ErrComposeFileNotFound
	}

	if len(a.ComposeFiles) > 1 {
		logger.Info("warning: multiple compose files found, only the first one will be used", zap.String("compose files", strings.Join(a.ComposeFiles, ",")))
	}

	storeInfo, err := a.StoreInfo(true)
	if err != nil {
		return err
	}

	if storeInfo == nil || storeInfo.StoreAppID == nil || *storeInfo.StoreAppID == "" {
		return ErrStoreInfoNotFound
	}

	// nil means no catalogue entry, which is an answer rather than a failure
	storeComposeApp, err := MyService.AppStoreManagement().ComposeApp(*storeInfo.StoreAppID)
	if err != nil {
		return err
	}

	newComposeYAML, err := a.composeYAMLForUpdate(storeComposeApp)
	if err != nil {
		return err
	}

	// prepare for message bus events
	eventProperties := common.PropertiesFromContext(ctx)
	eventProperties[common.PropertyTypeAppName.Name] = a.Name

	if err := a.UpdateEventPropertiesFromStoreInfo(eventProperties); err != nil {
		logger.Info("failed to update event properties from store info", zap.Error(err), zap.String("name", a.Name))
	}

	go func(ctx context.Context) {
		go PublishEventWrapper(ctx, common.EventTypeAppUpdateBegin, nil)

		defer PublishEventWrapper(ctx, common.EventTypeAppUpdateEnd, nil)

		MyService.AppStoreManagement().StartUpgrade(a.Name)
		defer MyService.AppStoreManagement().FinishUpgrade(a.Name)

		if err := a.PullAndApply(ctx, newComposeYAML); err != nil {
			go PublishEventWrapper(ctx, common.EventTypeAppUpdateError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			logger.Error("failed to update compose app", zap.Error(err), zap.String("name", a.Name))
			return
		}

		// The update applied. app:update-end is published whether this succeeded or
		// failed, so without this it says nothing at all and the dashboard has no
		// success to report -- which is why a finished update used to pass in silence.
		eventProperties[common.PropertyTypeAppUpdated.Name] = "true"
	}(ctx)

	return nil
}

// composeYAMLForUpdate is the docker-compose.yml an update writes, which depends on
// where the app came from.
//
// An app with a catalogue entry takes that catalogue's images: it is what says which
// version the app should be on. An app with none -- an imported compose file, one
// written by hand -- has nothing to consult, so it keeps the images it already names
// and the update is the pull that follows: the tags stay the same, the digests they
// resolve to need not.
//
// A nil storeComposeApp means the catalogue holds nothing for this app.
//
// That, not the `is_uncontrolled` flag, is the test. The flag means something else: it
// is set when an app's main tag has been moved off the catalogue's, and an imported
// app is written with it FALSE (see IsNewComposeUncontrolled, which returns false when
// it finds no store entry). Keying on the flag left imported apps exactly as frozen as
// before and took the catalogue away from pinned apps, which are the only ones that
// have one.
func (a *ComposeApp) composeYAMLForUpdate(storeComposeApp *ComposeApp) ([]byte, error) {
	if storeComposeApp == nil {
		return a.refreshedComposeYAML()
	}

	return a.updatedComposeYAML(storeComposeApp)
}

// refreshedComposeYAML is the file on disk, unchanged. It is built from the editing
// load rather than marshalled from a, for the reason spelled out on
// updatedComposeYAML: a is the runtime project, with `.env` already resolved, and
// marshalling it bakes every secret, published port and host path into the compose
// file and leaves `.env` dead.
func (a *ComposeApp) refreshedComposeYAML() ([]byte, error) {
	keys, err := a.EnvKeys()
	if err != nil {
		return nil, err
	}

	local, err := LoadComposeAppForEditing(a.Name, a.ComposeFiles[0], keys)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(local)
}

// updatedComposeYAML is the docker-compose.yml an update writes: the file on disk with the image of
// each service replaced by the store's. It is built from the editing load of that file, never from
// a itself: a is the runtime project (List), with `.env` resolved, and marshalling it bakes every
// secret, published port and host path into the compose file, leaving `.env` dead after the first
// App Store update. An unreadable `.env` is an error for the same reason.
func (a *ComposeApp) updatedComposeYAML(storeComposeApp *ComposeApp) ([]byte, error) {
	keys, err := a.EnvKeys()
	if err != nil {
		return nil, err
	}

	local, err := LoadComposeAppForEditing(a.Name, a.ComposeFiles[0], keys)
	if err != nil {
		return nil, err
	}

	// Services the local app has and the catalogue does not are the owner's own: a VPN
	// sidecar wired in by hand, a companion container. An update has nothing to say
	// about them, so it leaves them exactly as written rather than refusing -- refusing
	// meant that adding one container to a store app froze it forever, with nothing in
	// the interface saying why.
	_, storeAbsentOfLocal := lo.Difference(sortedServiceNames(local.Services), sortedServiceNames(storeComposeApp.Services))

	// The other direction is still a refusal: the catalogue has grown a service this
	// app does not run, and creating one is not something an update does yet. The error
	// names them and says why, because an interface that can only say `compose app not
	// match` leaves the owner with a button that fails and no reason.
	if len(storeAbsentOfLocal) > 0 {
		return nil, fmt.Errorf("%w: the app store version of %s adds services this app does not have (%s), and an update cannot create them",
			ErrComposeAppNotMatch, a.Name, strings.Join(storeAbsentOfLocal, ", "))
	}

	for name, service := range storeComposeApp.Services {
		localComposeAppService := local.Services[name] // present: storeAbsentOfLocal is empty

		// A tag republished under the same name says nothing about which image to run,
		// so the local reference stays and the pull is what moves it. This used to be a
		// loop over NeedCheckDigestTags whose LAST iteration won: with a second entry
		// in that list, any tag but the last one would have had its image replaced
		// anyway.
		if _, tag := docker.ExtractImageAndTag(service.Image); !lo.Contains(common.NeedCheckDigestTags, tag) {
			localComposeAppService.Image = service.Image
		}

		local.Services[name] = localComposeAppService
	}

	// the code is need by stable diffusion.
	removeRuntime(local)

	return yaml.Marshal(local)
}

// TODO rename the function to service and add error return value
func (a *ComposeApp) App(name string) *App {
	if name == "" {
		return nil
	}

	service, ok := a.Services[name]
	if !ok {
		return nil
	}
	service.Name = name
	app := App(service)

	return &app
}

func (a *ComposeApp) Apps() map[string]*App {
	apps := make(map[string]*App, len(a.Services))

	for _, name := range sortedServiceNames(a.Services) {
		apps[name] = a.App(name)
	}

	return apps
}

func sortedServiceNames(services types.Services) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *ComposeApp) MainService() (*App, error) {
	storeInfo, err := a.StoreInfo(false)
	if err != nil {
		return nil, err
	}

	if storeInfo.Main == nil || *storeInfo.Main == "" {
		return nil, ErrMainServiceNotSpecified
	}

	return a.App(*storeInfo.Main), nil
}

func (a *ComposeApp) MainTag() (string, error) {
	mainService, err := a.MainService()
	if err != nil {
		return "", err
	}
	_, newTag := docker.ExtractImageAndTag(mainService.Image)

	return newTag, nil
}

func (a *ComposeApp) Containers(ctx context.Context) (map[string][]api.ContainerSummary, error) {
	service, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	containers, err := service.Ps(ctx, a.Name, api.PsOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}

	// it is possible a `service` contains multiple containers.
	// See https://docs.docker.com/compose/compose-file/deploy/#replicas
	return lo.GroupBy(containers, func(container api.ContainerSummary) string {
		return container.Service
	}), nil
}

func (a *ComposeApp) Pull(ctx context.Context) error {
	// pull
	serviceNum := len(a.Services)

	serviceNames := sortedServiceNames(a.Services)
	for i, name := range serviceNames {
		app := a.Services[name]
		if err := func() error {
			go PublishEventWrapper(ctx, common.EventTypeImagePullBegin, map[string]string{
				common.PropertyTypeImageName.Name: app.Image,
			})

			defer PublishEventWrapper(ctx, common.EventTypeImagePullEnd, map[string]string{
				common.PropertyTypeImageName.Name: app.Image,
			})

			if err := docker.PullImage(ctx, app.Image, func(out io.ReadCloser) {
				pullImageProgress(ctx, out, "INSTALL", serviceNum, i+1)
			}); err != nil {
				go PublishEventWrapper(ctx, common.EventTypeImagePullError, map[string]string{
					common.PropertyTypeImageName.Name: app.Image,
					common.PropertyTypeMessage.Name:   err.Error(),
				})
			}

			return nil
		}(); err != nil {
			return err
		}
	}

	return nil
}

func (a *ComposeApp) injectEnvVariableToComposeApp() {
	for name, service := range a.Services {
		if service.Environment == nil {
			service.Environment = types.MappingWithEquals{}
		}
		for k, v := range config.Global {
			// if there is same name var declared in environment in compose yaml
			// we should not reassign a value to it.
			if service.Environment[k] == nil {
				service.Environment[k] = utils.Ptr(v)
			}
		}
		a.Services[name] = service
	}
}

// upWaitTimeout bounds how long we wait for an app's containers to become
// running-or-healthy. A var, not a const, so the test can shorten it.
//
// The pull already happened by the time we get here, so this budget covers create,
// start and healthcheck convergence only. The other container waits in this service
// are 20s (waiting for an app to be fully exited) and the HTTP checks are 20-30s;
// the one long wait, 30 minutes, is the boot window for /DATA to mount, which is a
// different kind of waiting. Five minutes sits between them on purpose: enough for a
// dependency chain of healthchecks with 60s start_periods on a slow ARM box, short
// enough that a stack which will never converge is diagnosed and rolled back while
// the owner is still looking at the page.
var upWaitTimeout = 5 * time.Minute

// waitForApp runs a compose operation that was asked to wait for containers, under a
// deadline, and reports a blown deadline as the failure it is.
//
// Both halves are needed. api.StartOptions.Wait polls every 500ms for every service
// to be running-or-healthy and only wraps the context in a deadline of its own when
// WaitTimeout > 0 (pkg/compose/start.go), so without one it never gives up: a gluetun
// stack, whose services cannot start until the VPN container is healthy, parks here
// for ever and the caller's rollback never runs. And compose v2.27 swallows the
// cancellation -- waitDependencies returns nil for every service once the context is
// done (pkg/compose/convergence.go), and progress.Run passes that nil straight
// through -- so a wait that timed out comes back as success unless we look.
func waitForApp(ctx context.Context, name string, up func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, upWaitTimeout)
	defer cancel()

	if err := up(ctx); err != nil {
		return err
	}

	if ctx.Err() != nil {
		return fmt.Errorf("compose app `%s` was not running or healthy after %s", name, upWaitTimeout)
	}

	return nil
}

func (a *ComposeApp) Up(ctx context.Context, service api.Service) error {
	a.injectEnvVariableToComposeApp()

	// no OnExit: compose only reads it when Start.Attach is set, and we never attach
	// (pkg/compose/up.go) -- CascadeStop here was decoration.
	if err := waitForApp(ctx, a.Name, func(ctx context.Context) error {
		return service.Up(ctx, (*codegen.ComposeApp)(a), api.UpOptions{
			Start: api.StartOptions{
				Wait: true,
			},
		})
	}); err != nil {
		logger.Error("failed to start original compose app", zap.Error(err), zap.String("name", a.Name))
		return err
	}
	return nil
}

func (a *ComposeApp) UpWithCheckRequire(ctx context.Context, service api.Service) error {
	// prepare source path for volumes if not exist
	for name, app := range a.Services {
		for _, volume := range app.Volumes {
			if _, ok := a.Volumes[volume.Source]; ok {
				// this is a internal volume, so skip.
				continue
			}

			path := volume.Source
			if err := file.IsNotExistMkDir(path); err != nil {
				go PublishEventWrapper(ctx, common.EventTypeContainerStartError, map[string]string{
					common.PropertyTypeMessage.Name: err.Error(),
				})
				return err
			}
		}

		// check if each required device exists
		deviceMapFiltered := []string{}
		for _, deviceMap := range app.Devices {
			devicePath := strings.SplitN(deviceMap, ":", 2)[0]
			if file.CheckNotExist(devicePath) {
				logger.Info("device not found", zap.String("device", devicePath))
				continue
			}
			deviceMapFiltered = append(deviceMapFiltered, deviceMap)
		}
		app.Devices = deviceMapFiltered
		a.Services[name] = app
	}

	if err := a.Up(ctx, service); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeContainerStartError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})
		return err
	}
	return nil
}

func (a *ComposeApp) PullAndApply(ctx context.Context, newComposeYAML []byte) error {
	return a.pullAndApply(ctx, newComposeYAML, nil)
}

// pullAndApply writes the new compose file and, when newEnv is not nil, the new `.env` (empty
// removes it), then pulls and (re)creates the app; both files are put back if that fails, so disk
// and running state never diverge.
func (a *ComposeApp) pullAndApply(ctx context.Context, newComposeYAML []byte, newEnv *[]byte) error {
	// backup current compose file
	currentComposeFile := a.ComposeFiles[0]

	backupComposeFile := currentComposeFile + "." + "bak"
	if err := file.CopySingleFile(currentComposeFile, backupComposeFile, ""); err != nil {
		return err
	}

	var oldEnv []byte // nil when there was none: WriteEnvFile(nil) removes
	if newEnv != nil {
		var err error
		if oldEnv, err = a.EnvFileText(); err != nil {
			return err
		}
	}

	// start compose app
	service, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	success := false

	defer func() {
		if !success {
			if err := file.CopySingleFile(backupComposeFile, currentComposeFile, ""); err != nil {
				logger.Error("failed to restore original compose file", zap.Error(err), zap.String("src", backupComposeFile), zap.String("dst", currentComposeFile))
				return
			}

			if newEnv != nil {
				if err := a.WriteEnvFile(oldEnv); err != nil {
					logger.Error("failed to restore original .env", zap.Error(err), zap.String("path", a.EnvFile()))
					return
				}
			}

			if err := a.Up(ctx, service); err != nil {
				logger.Error("failed to start original compose app", zap.Error(err), zap.String("name", a.Name))
				return
			}

		}
	}()

	// save new compose file
	if err := file.WriteToFullPath(newComposeYAML, currentComposeFile, 0o600); err != nil {
		return err
	}

	if newEnv != nil {
		if err := a.WriteEnvFile(*newEnv); err != nil {
			return err
		}
	}

	newComposeApp, err := LoadComposeAppFromConfigFile(a.Name, currentComposeFile)
	if err != nil {
		return err
	}

	if err := newComposeApp.Pull(ctx); err != nil {
		return err
	}

	go PublishEventWrapper(ctx, common.EventTypeContainerStartBegin, nil)

	defer PublishEventWrapper(ctx, common.EventTypeContainerStartEnd, nil)

	// an app that fails to start counts as a failed apply: the files are put back and the
	// previous app started from them, so disk and running state never diverge
	err = newComposeApp.UpWithCheckRequire(ctx, service)
	success = err == nil
	return err
}

func (a *ComposeApp) Create(ctx context.Context, options api.CreateOptions, service api.Service) error {
	a.injectEnvVariableToComposeApp()
	return service.Create(ctx, (*codegen.ComposeApp)(a), api.CreateOptions{})
}

func (a *ComposeApp) PullAndInstall(ctx context.Context) error {
	service, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	// pull
	if err := a.Pull(ctx); err != nil {
		return err
	}

	// create
	if err := func() error {
		go PublishEventWrapper(ctx, common.EventTypeContainerCreateBegin, nil)

		defer PublishEventWrapper(ctx, common.EventTypeContainerCreateEnd, nil)

		for name, app := range a.Services {
			// prepare source path for volumes if not exist
			for _, volume := range app.Volumes {
				if _, ok := a.Volumes[volume.Source]; ok {
					// this is a internal volume, so skip.
					continue
				}

				path := volume.Source
				if err := file.IsNotExistMkDir(path); err != nil {
					go PublishEventWrapper(ctx, common.EventTypeContainerCreateError, map[string]string{
						common.PropertyTypeMessage.Name: err.Error(),
					})
					return err
				}
			}

			// check if each required device exists
			deviceMapFiltered := []string{}
			for _, deviceMap := range app.Devices {
				devicePath := strings.SplitN(deviceMap, ":", 2)[0]
				if file.CheckNotExist(devicePath) {
					logger.Info("device not found", zap.String("device", devicePath))
					continue
				}
				deviceMapFiltered = append(deviceMapFiltered, deviceMap)
			}
			app.Devices = deviceMapFiltered
			a.Services[name] = app
		}

		if err := a.Create(ctx, api.CreateOptions{}, service); err != nil {
			go PublishEventWrapper(ctx, common.EventTypeContainerCreateError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
			return err
		}

		return nil
	}(); err != nil {
		return err
	}

	go PublishEventWrapper(ctx, common.EventTypeContainerStartBegin, nil)

	defer PublishEventWrapper(ctx, common.EventTypeContainerStartEnd, nil)

	if err := waitForApp(ctx, a.Name, func(ctx context.Context) error {
		return service.Start(ctx, a.Name, api.StartOptions{Wait: true})
	}); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeContainerStartError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})
		return err
	}

	return nil
}

func (a *ComposeApp) Uninstall(ctx context.Context, deleteConfigFolder bool) error {
	service, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	// stop
	if err := func() error {
		go PublishEventWrapper(ctx, common.EventTypeContainerStopBegin, nil)

		defer PublishEventWrapper(ctx, common.EventTypeContainerStopEnd, nil)

		if err := service.Stop(ctx, a.Name, api.StopOptions{}); err != nil {
			go PublishEventWrapper(ctx, common.EventTypeContainerStopError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			return err
		}

		return nil
	}(); err != nil {
		return err
	}

	// remove
	go PublishEventWrapper(ctx, common.EventTypeContainerRemoveBegin, nil)

	defer PublishEventWrapper(ctx, common.EventTypeContainerRemoveEnd, nil)

	if err := service.Down(ctx, a.Name, api.DownOptions{
		RemoveOrphans: true,
		Images:        "all",
		Volumes:       true,
	}); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})

		return err
	}

	if err := file.RMDir(a.WorkingDir); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})
	}

	if !deleteConfigFolder {
		return nil
	}

	for _, app := range a.Services {
		for _, volume := range app.Volumes {
			if strings.Contains(volume.Source, a.Name) {
				path := filepath.Join(strings.Split(volume.Source, a.Name)[0], a.Name)
				if err := file.RMDir(path); err != nil {
					logger.Error("failed to remove compose app config folder", zap.Error(err), zap.String("path", path))

					go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
						common.PropertyTypeMessage.Name: err.Error(),
					})
				}
			}
		}
	}

	return nil
}

func (a *ComposeApp) Apply(ctx context.Context, newComposeYAML []byte) error {
	return a.apply(ctx, newComposeYAML, nil)
}

// ApplyEnv replaces the `.env` of the app (empty removes it) and re-creates it from its unchanged
// compose file: a restart would keep the old environment, the apply reloads the project first.
func (a *ComposeApp) ApplyEnv(ctx context.Context, env []byte) error {
	if len(a.ComposeFiles) <= 0 {
		return ErrComposeFileNotFound
	}

	current, err := os.ReadFile(a.ComposeFiles[0])
	if err != nil {
		return err
	}

	return a.apply(ctx, current, &env)
}

func (a *ComposeApp) apply(ctx context.Context, newComposeYAML []byte, newEnv *[]byte) error {
	// compare new ComposeApp with current ComposeApp
	if getNameFrom(newComposeYAML) != a.Name {
		return ErrComposeAppNotMatch
	}

	newComposeApp, err := NewComposeAppFromYAML(newComposeYAML, true, true)
	if err != nil {
		return err
	}

	if len(a.ComposeFiles) <= 0 {
		return ErrComposeFileNotFound
	}

	if len(a.ComposeFiles) > 1 {
		logger.Info("warning: multiple compose files found, only the first one will be used", zap.String("compose files", strings.Join(a.ComposeFiles, ",")))
	}

	// prepare for message bus events
	eventProperties := common.PropertiesFromContext(ctx)
	eventProperties[common.PropertyTypeAppName.Name] = a.Name

	// prepare for message bus events
	if err := newComposeApp.UpdateEventPropertiesFromStoreInfo(eventProperties); err != nil {
		logger.Info("failed to update event properties from store info", zap.Error(err), zap.String("name", a.Name))
	}

	go func(ctx context.Context) {
		go PublishEventWrapper(ctx, common.EventTypeAppApplyChangesBegin, nil)

		defer PublishEventWrapper(ctx, common.EventTypeAppApplyChangesEnd, nil)

		if err := a.pullAndApply(ctx, newComposeYAML, newEnv); err != nil {
			go PublishEventWrapper(ctx, common.EventTypeAppApplyChangesError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			logger.Error("failed to apply changes to compose app", zap.Error(err), zap.String("name", a.Name))
		}
	}(ctx)

	return nil
}

func (a *ComposeApp) SetStatus(ctx context.Context, status codegen.RequestComposeAppStatus) error {
	service, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	eventProperties := common.PropertiesFromContext(ctx)
	if eventProperties == nil {
		eventProperties = map[string]string{}
	}
	eventProperties[common.PropertyTypeAppName.Name] = a.Name

	switch status {
	case codegen.RequestComposeAppStatusStart:
		go func(ctx context.Context) {
			go PublishEventWrapper(ctx, common.EventTypeAppStartBegin, nil)

			defer PublishEventWrapper(ctx, common.EventTypeAppStartEnd, nil)

			// the user wants the app running again, drop any stale marker
			clearAppStopped(a.Name)

			// to make sure the container is stopped
			// timeout is 20s
			for index := 0; index < 10; index++ {
				containerSummarys, err := service.Ps(ctx, a.Name, api.PsOptions{
					All: true,
				})
				if err != nil {
					logger.Error("failed to get compose app info", zap.Error(err), zap.String("name", a.Name))
				}
				isContainerExited := true
				for _, containerSummary := range containerSummarys {
					// to make sure every service of the container is stopped
					// I think "exited" can be replace by constant value.
					isContainerExited = isContainerExited && (containerSummary.State == "exited")
				}
				if isContainerExited {
					break
				}
				time.Sleep(2 * time.Second)
			}

			if err := waitForApp(ctx, a.Name, func(ctx context.Context) error {
				return service.Start(ctx, a.Name, api.StartOptions{Wait: true})
			}); err != nil {
				go PublishEventWrapper(ctx, common.EventTypeAppStartError, map[string]string{
					common.PropertyTypeMessage.Name: err.Error(),
				})

				logger.Error("failed to start compose app", zap.Error(err), zap.String("name", a.Name))
			}
		}(ctx)
	case codegen.RequestComposeAppStatusStop:
		go func(ctx context.Context) {
			go PublishEventWrapper(ctx, common.EventTypeAppStopBegin, nil)

			defer PublishEventWrapper(ctx, common.EventTypeAppStopEnd, nil)

			if err := service.Stop(ctx, a.Name, api.StopOptions{}); err != nil {
				go PublishEventWrapper(ctx, common.EventTypeAppStopError, map[string]string{
					common.PropertyTypeMessage.Name: err.Error(),
				})

				logger.Error("failed to stop compose app", zap.Error(err), zap.String("name", a.Name))
				return
			}

			markAppStopped(a.Name)
		}(ctx)
	case codegen.RequestComposeAppStatusRestart:
		go func(ctx context.Context) {
			go PublishEventWrapper(ctx, common.EventTypeAppRestartBegin, nil)

			defer PublishEventWrapper(ctx, common.EventTypeAppRestartEnd, nil)

			clearAppStopped(a.Name)

			if err := service.Restart(ctx, a.Name, api.RestartOptions{}); err != nil {
				go PublishEventWrapper(ctx, common.EventTypeAppRestartError, map[string]string{
					common.PropertyTypeMessage.Name: err.Error(),
				})

				logger.Error("failed to restart compose app", zap.Error(err), zap.String("name", a.Name))
			}
		}(ctx)
	default:
		return ErrInvalidComposeAppStatus
	}

	return nil
}

func (a *ComposeApp) Logs(ctx context.Context, lines int) ([]byte, error) {
	service, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	var buf bytes.Buffer

	// Timestamps come from the daemon (per line, when it was actually written) rather
	// than from the consumer's own timestamp flag, which stamps time.Now() at read time
	// and would give every line of a tail the same value.
	consumer := formatter.NewLogConsumer(ctx, &buf, &buf, false, true, false)

	if err := service.Logs(ctx, a.Name, consumer, api.LogOptions{
		Project:    (*codegen.ComposeApp)(a),
		Services:   sortedServiceNames(a.Services),
		Follow:     false,
		Timestamps: true,
		Tail:       lo.If(lines < 0, "all").Else(strconv.Itoa(lines)),
	}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (a *ComposeApp) GetPortsInUse() (*codegen.ComposeAppValidationErrorsPortsInUse, error) {
	tcpPorts, udpPorts, err := port.ListPortsInUse()
	if err != nil {
		return nil, err
	}

	allPortsInUse := lo.Union(tcpPorts, udpPorts)

	tcpPortInUse := []string{}
	udpPortInUse := []string{}

	for _, s := range a.Services {
		for _, p := range s.Ports {
			if lo.ContainsBy(allPortsInUse, func(portInUse int) bool { return strconv.Itoa(portInUse) == p.Published }) {
				switch strings.ToLower(p.Protocol) {
				case "tcp":
					tcpPortInUse = append(tcpPortInUse, p.Published)
				case "udp":
					udpPortInUse = append(udpPortInUse, p.Published)
				}
			}
		}
	}

	if len(tcpPortInUse) == 0 && len(udpPortInUse) == 0 {
		return nil, nil
	}

	portsInUse := struct {
		TCP *codegen.PortList "json:\"TCP,omitempty\""
		UDP *codegen.PortList "json:\"UDP,omitempty\""
	}{TCP: &tcpPortInUse, UDP: &udpPortInUse}

	return &codegen.ComposeAppValidationErrorsPortsInUse{PortsInUse: &portsInUse}, nil
}

// Try to update AppIcon and AppTitle in given event properties from store info
func (a *ComposeApp) UpdateEventPropertiesFromStoreInfo(eventProperties map[string]string) error {
	if eventProperties == nil {
		return fmt.Errorf("event properties is nil")
	}

	storeInfo, err := a.StoreInfo(false)
	if err != nil {
		return err
	}

	eventProperties[common.PropertyTypeAppIcon.Name] = storeInfo.Icon

	if storeInfo.Title == nil {
		return fmt.Errorf("compose app title not found in store info")
	}

	titles, err := json.Marshal(storeInfo.Title)
	if err != nil {
		return err
	}

	eventProperties[common.PropertyTypeAppTitle.Name] = string(titles)

	return nil
}

func (a *ComposeApp) HealthCheck() (bool, error) {
	storeInfo, err := a.StoreInfo(false)
	if err != nil {
		return false, err
	}

	scheme := "http"
	if storeInfo.Scheme != nil && *storeInfo.Scheme != "" {
		scheme = string(*storeInfo.Scheme)
	}

	hostname := common.Localhost
	if storeInfo.Hostname != nil && *storeInfo.Hostname != "" {
		hostname = *storeInfo.Hostname
	}

	url := fmt.Sprintf(
		"%s://%s:%s/%s",
		scheme,
		hostname,
		storeInfo.PortMap,
		strings.TrimLeft(storeInfo.Index, "/"),
	)

	logger.Info("checking compose app health at the specified web port...", zap.String("name", a.Name), zap.Any("url", url))

	client := resty.New()
	client.SetTimeout(30 * time.Second)
	client.SetHeader("Accept", "text/html")
	// ignore ssl error
	client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
	response, err := client.R().Get(url)
	if err != nil {
		logger.Error("failed to check container health", zap.Error(err), zap.String("name", a.Name))
		return false, err
	}
	if response.StatusCode() == http.StatusOK || response.StatusCode() == http.StatusUnauthorized {
		return true, nil
	}

	logger.Error("compose app health check failed at the specified web port", zap.Any("name", a.Name), zap.Any("url", url), zap.String("status", fmt.Sprint(response.StatusCode())))
	return false, nil
}

func LoadComposeAppFromConfigFile(appID string, configFile string) (*ComposeApp, error) {
	options := composeCmd.ProjectOptions{
		ProjectDir:  filepath.Dir(configFile),
		ProjectName: appID,
	}

	env := []string{fmt.Sprintf("%s=%s", "AppID", appID)}
	for k, v := range baseInterpolationMap() {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// load project
	project, _, err := options.ToProject(
		context.Background(),
		nil,
		nil,
		cli.WithWorkingDirectory(options.ProjectDir), // this has to be the first option, otherwise it will assume the dir where this program is running is the working directory.

		cli.WithOsEnv,
		cli.WithDotEnv,
		cli.WithEnv(env),
		cli.WithConfigFileEnv,
		cli.WithDefaultConfigPath,
		cli.WithEnvFiles(options.EnvFiles...),
		cli.WithName(options.ProjectName),
	)

	return (*ComposeApp)(project), err
}

var gpuCache *([]external.NvidiaGPUInfo) = nil

func removeRuntime(a *ComposeApp) {
	if config.RemoveRuntimeIfNoNvidiaGPUFlag {

		// if gpuCache is nil, it means it is first time fetching gpu info
		if gpuCache == nil {
			value, err := external.NvidiaGPUInfoList()
			if err != nil {
				gpuCache = &([]external.NvidiaGPUInfo{})
			} else {
				gpuCache = &value
			}

			// without nvidia-smi 	// no gpu or first time fetching gpu info failed
		}
		if len(*gpuCache) == 0 {
			for name, service := range a.Services {
				service.Runtime = ""
				a.Services[name] = service
			}
		}
	}
}

func NewComposeAppFromYAML(yaml []byte, skipInterpolation, skipValidation bool) (*ComposeApp, error) {
	return newComposeAppFromYAML(yaml, skipInterpolation, skipValidation, nil)
}

// keep is non-nil for the settings pipeline (ComposeAppFromSettingsYAML): the output is compose
// text again, see substituteEscaping. nil resolves everything as before.
func newComposeAppFromYAML(yaml []byte, skipInterpolation, skipValidation bool, keep map[string]struct{}) (*ComposeApp, error) {
	tmpWorkingDir, err := os.MkdirTemp("", "casaos-compose-app-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpWorkingDir)

	// the WEBUI_PORT interpolate will tiger twice. In `pulished` and `port-map`.
	// So we need to promise multiple WEBUI_PORT interpolate is a same value.
	port, _ := port.GetAvailablePort("tcp")

	project, err := loader.Load(
		types.ConfigDetails{
			ConfigFiles: []types.ConfigFile{
				{
					Content: []byte(yaml),
				},
			},
			Environment: keepRefs(keep),

			// need to set a working dir because loader/normalize.go from github.com/compose-spec/compose-go makes
			// wrong assumption that the working dir is the same as the dir where this program is launched.
			WorkingDir: tmpWorkingDir,
		},
		func(o *loader.Options) {
			o.SkipInterpolation = skipInterpolation
			o.SkipValidation = skipValidation
			if keep != nil {
				keepPathRefs(o)
				keepRefCasts(o)
				o.Interpolate.Substitute = substituteEscaping(settingsKeep(keep))
			}

			o.Interpolate.LookupValue = func(key string) (string, bool) {
				switch key {
				case "WEBUI_PORT":
					fmt.Printf("WEBUI_PORT is not specified, using %d\n", port)
					return strconv.Itoa(port), true
				}

				for k := range baseInterpolationMap() {
					if k == key {
						// example:  TZ => $TZ
						// we didn't want to interpolate base interpolation value.
						// they should be interpolated in LoadComposeAppFromConfig
						return fmt.Sprintf("$%s", k), true
					}
				}
				// the function may can to replace the above code.
				value, ok := os.LookupEnv(key)
				if ok {
					return value, true
				} else {
					return fmt.Sprintf("$%s", key), true
				}
			}

			if getNameFrom(yaml) != "" {
				return
			}

			// fix compose app name
			logger.Info("compose app name is not specified, getting a name from one of our contributors :)")
			projectName := random.Name(nil)
			logger.Info("compose app name is given", zap.String("name", projectName))
			o.SetProjectName(projectName, false)
		},
	)
	if err != nil {
		return nil, err
	}

	composeApp := (*ComposeApp)(project)

	if composeApp.Extensions == nil {
		composeApp.Extensions = map[string]interface{}{}
	}

	storeInfo, err := composeApp.StoreInfo(false)

	if err != nil || storeInfo == nil || storeInfo.Title == nil {
		logger.Info("compose app does not have store info with title set, re-using app name as title", zap.String("app", composeApp.Name))
		composeApp.SetTitle(composeApp.Name, common.DefaultLanguage)
	}

	removeRuntime(composeApp)

	// pass icon information to v1 label for backward compatibility, because we are
	// still using `func getContainerStats()` from `container.go` to get container stats
	// (we are being lazy to upgrade that v1 API to v2 - please help if you can :D)
	if err == nil && storeInfo != nil && storeInfo.Icon != "" {
		for name, service := range composeApp.Services {
			if service.Labels == nil {
				service.Labels = map[string]string{}
			}
			service.Labels[v1.V1LabelIcon] = storeInfo.Icon
			composeApp.Services[name] = service
		}
	}

	return composeApp, nil
}

func getNameFrom(composeYAML []byte) string {
	var baseStructure struct {
		Name string `yaml:"name"`
	}

	if err := yaml.Unmarshal(composeYAML, &baseStructure); err != nil {
		return ""
	}

	return baseStructure.Name
}

func (a *ComposeApp) SetUncontrolled(uncontrolled bool) error {
	xCasaos := a.Extensions[common.ComposeExtensionNameXCasaOS]
	xCasaosMap, ok := xCasaos.(map[string]interface{})

	// set to controlled app
	if !ok {
		logger.Error("failed to get map compose app extensions", zap.String("composeAppID", a.Name))
		return ErrComposeExtensionNameXCasaOSNotFound
	} else {
		xCasaosMap[common.ComposeExtensionPropertyNameIsUncontrolled] = uncontrolled
		a.Extensions[common.ComposeExtensionNameXCasaOS] = xCasaosMap
	}

	return nil
}
