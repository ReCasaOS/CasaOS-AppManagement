package v2

import (
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/model"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

func (a *AppManagement) GetAppGrid(ctx echo.Context) error {
	// v2 Apps
	composeAppsWithStoreInfo, err := composeAppsWithStoreInfo(ctx.Request().Context(), composeAppsWithStoreInfoOpts{
		checkIsUpdateAvailable: false,
	})
	if err != nil {
		message := err.Error()
		logger.Error("failed to list compose apps with store info", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	// Read once, for two readers: the port an app with no `x-casaos` can be opened on,
	// and the list below that tells a loose container from one a compose app owns.
	containersByApp := map[string]map[string][]codegen.ContainerSummary{}
	composeAppContainers := []codegen.ContainerSummary{}
	for id, app := range composeAppsWithStoreInfo {
		composeApp := (service.ComposeApp)(*app.Compose)
		containerLists, err := composeApp.Containers(ctx.Request().Context())
		if err != nil {
			// One app Docker cannot answer about is not the whole grid. This used to
			// `return nil`, which in an echo handler is a 200 with no body at all --
			// so a single unanswerable app emptied the dashboard rather than losing
			// itself from it.
			logger.Error("failed to get containers for compose app", zap.Error(err), zap.String("app", composeApp.Name))
			continue
		}

		containersByApp[id] = containerLists
		for _, containcontainerList := range containerLists {
			composeAppContainers = append(composeAppContainers, containcontainerList...)
		}
	}

	v2AppGridItems := lo.FilterMap(lo.Values(composeAppsWithStoreInfo), func(app codegen.ComposeAppWithStoreInfo, i int) (codegen.WebAppGridItem, bool) {
		item, err := WebAppGridItemAdapterV2(&app)
		if err != nil {
			logger.Error("failed to adapt web app grid item", zap.Error(err), zap.String("app", app.Compose.Name))
			return codegen.WebAppGridItem{}, false
		}

		// A stack written by hand declares no web interface, so the card had nothing to
		// open. Docker knows which ports it publishes, and for most such stacks that is
		// the whole answer. Settings > Web UI still wins wherever it is filled in --
		// this only speaks when nothing else has.
		if item.Port == nil || *item.Port == "" {
			composeApp := (*service.ComposeApp)(app.Compose)
			if port := inferWebUIPort(containersByApp[composeApp.Name], composeApp.MainServiceName()); port != "" {
				item.Port = &port
			}
		}

		return *item, true
	})

	// v1 Apps
	casaOSApps, containers := service.MyService.Docker().GetContainerAppList(nil, nil, nil)

	v1AppGridItems := lo.Map(*casaOSApps, func(app model.MyAppList, i int) codegen.WebAppGridItem {
		item, err := WebAppGridItemAdapterV1(&app)
		if err != nil {
			logger.Error("failed to adapt web app grid item", zap.Error(err), zap.String("app", app.Name))
			return codegen.WebAppGridItem{}
		}
		return *item
	})

	containerAppGridItems := lo.FilterMap(*containers, func(app model.MyAppList, i int) (codegen.WebAppGridItem, bool) {
		if lo.ContainsBy(composeAppContainers, func(container codegen.ContainerSummary) bool { return container.ID == app.ID }) {
			// already exists as compose app, skipping...
			return codegen.WebAppGridItem{}, false
		}

		// check if this is a replacement container for a compose app when applying new settings or updating.
		//
		// we need this logic so that user does not see the temporary replacement container in the UI.
		{
			container, err := service.MyService.Docker().GetContainerByName(app.Name)
			if err != nil {
				logger.Error("failed to get container by name", zap.Error(err), zap.String("container", app.Name))
				return codegen.WebAppGridItem{}, false
			}

			// see recreateContainer() func from https://github.com/docker/compose/blob/v2/pkg/compose/convergence.go
			if replaceLabel, ok := container.Labels[api.ContainerReplaceLabel]; ok {
				if lo.ContainsBy(
					composeAppContainers,
					func(container codegen.ContainerSummary) bool {
						return container.ID == replaceLabel
					},
				) {
					// this is a replacement container for a compose app, skipping...
					return codegen.WebAppGridItem{}, false
				}
			}
		}

		item, err := WebAppGridItemAdapterContainer(&app)
		if err != nil {
			logger.Error("failed to adapt web app grid item", zap.Error(err), zap.String("app", app.Name))
			return codegen.WebAppGridItem{}, false
		}
		return *item, true
	})

	// merge v1 and v2 apps
	var appGridItems []codegen.WebAppGridItem
	appGridItems = append(appGridItems, v2AppGridItems...)
	appGridItems = append(appGridItems, v1AppGridItems...)
	appGridItems = append(appGridItems, containerAppGridItems...)

	return ctx.JSON(http.StatusOK, codegen.GetWebAppGridOK{
		Message: utils.Ptr("This data is for internal use ONLY - will not be supported for public use."),
		Data:    &appGridItems,
	})
}

func WebAppGridItemAdapterV2(composeAppWithStoreInfo *codegen.ComposeAppWithStoreInfo) (*codegen.WebAppGridItem, error) {
	if composeAppWithStoreInfo == nil {
		return nil, fmt.Errorf("v2 compose app is nil")
	}

	// validation
	composeApp := (*service.ComposeApp)(composeAppWithStoreInfo.Compose)
	if composeApp == nil {
		return nil, fmt.Errorf("failed to get compose app")
	}

	item := &codegen.WebAppGridItem{
		AppType: codegen.V2app,
		Name:    &composeApp.Name,
		Title: lo.ToPtr(map[string]string{
			common.DefaultLanguage: composeApp.Name,
		}),
		IsUncontrolled: utils.Ptr(false),

		// What an app is DOING is not part of its store info, and a stack written by
		// hand has no store info at all. Copying this inside the block below meant such
		// an app reached the dashboard with no status, which it reads as an app that is
		// not running and draws greyed out -- on a stack whose containers are all up.
		// The caller has already worked the status out from every container of every
		// service; it only has to survive the trip.
		Status: composeAppWithStoreInfo.Status,

		// Read from the cache a check pass filled, so this stays a map lookup. Nil
		// until something has checked, which the dashboard renders as no badge
		// rather than as an app known to be current.
		UpdateAvailable: service.ImageUpdateAvailable(composeApp.Name),
	}

	composeAppStoreInfo := composeAppWithStoreInfo.StoreInfo
	if composeAppStoreInfo != nil {

		// item properties from store info
		item.Hostname = composeAppStoreInfo.Hostname
		item.Icon = &composeAppStoreInfo.Icon
		item.Index = &composeAppStoreInfo.Index
		item.Port = &composeAppStoreInfo.PortMap
		item.Scheme = composeAppStoreInfo.Scheme
		item.StoreAppID = composeAppStoreInfo.StoreAppID
		item.Title = &composeAppStoreInfo.Title
		item.IsUncontrolled = composeAppStoreInfo.IsUncontrolled

		// not `*composeAppStoreInfo.Main`: store info over an empty service list leaves
		// Main nil, and that dereference has the same shape as the one that took the
		// whole service down on a compose file with no `x-casaos`
		mainApp := composeApp.App(composeApp.MainServiceName())
		if mainApp != nil {
			item.Image = &mainApp.Image // Hengxin needs this image property for some reason...
		}
	}

	// item type
	itemAuthorType := composeApp.AuthorType()
	item.AuthorType = &itemAuthorType
	if composeAppWithStoreInfo.IsUncontrolled == nil {
		item.IsUncontrolled = utils.Ptr(false)
	} else {
		item.IsUncontrolled = composeAppWithStoreInfo.IsUncontrolled
	}

	return item, nil
}

func WebAppGridItemAdapterV1(app *model.MyAppList) (*codegen.WebAppGridItem, error) {
	if app == nil {
		return nil, fmt.Errorf("v1 app is nil")
	}

	item := &codegen.WebAppGridItem{
		AppType:  codegen.V1app,
		Name:     &app.ID,
		Status:   &app.State,
		Image:    &app.Image,
		Hostname: &app.Host,
		Icon:     &app.Icon,
		Index:    &app.Index,
		Port:     &app.Port,
		Scheme:   (*codegen.Scheme)(&app.Protocol),
		Title: &map[string]string{
			common.DefaultLanguage: app.Name,
		},
		IsUncontrolled: &app.IsUncontrolled,
	}

	return item, nil
}

func WebAppGridItemAdapterContainer(container *model.MyAppList) (*codegen.WebAppGridItem, error) {
	if container == nil {
		return nil, fmt.Errorf("container is nil")
	}

	item := &codegen.WebAppGridItem{
		AppType: codegen.Container,
		Name:    &container.ID,
		Status:  &container.State,
		Image:   &container.Image,
		Title: &map[string]string{
			common.DefaultLanguage: container.Name,
		},
		IsUncontrolled: &container.IsUncontrolled,
	}

	// Set only when a compose project claims the container. It reaches this adapter
	// only when the compose list did not claim it -- which it silently does not for a
	// project whose config file cannot be loaded -- so `container` here does not mean
	// "belongs to no stack", and the dashboard has to be told which it is before it
	// offers an operation that would clone the container out of its project.
	if container.ComposeProject != "" {
		item.ComposeProject = &container.ComposeProject
	}

	return item, nil
}
