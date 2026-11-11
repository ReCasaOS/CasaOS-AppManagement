package v1

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/model"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	v2 "github.com/ReCasaOS/CasaOS-AppManagement/route/v2"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	v1 "github.com/ReCasaOS/CasaOS-AppManagement/service/v1"
	modelCommon "github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-Common/utils/ssh"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/errdefs"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	CheckOrigin:      func(r *http.Request) bool { return true },
	HandshakeTimeout: time.Duration(time.Second * 5),
}

// 打开docker的terminal
func DockerTerminal(ctx echo.Context) error {
	col := v2.DefaultQuery(ctx, "cols", "100")
	row := v2.DefaultQuery(ctx, "rows", "30")
	conn, err := upgrader.Upgrade(ctx.Response().Writer, ctx.Request(), nil)
	if err != nil {
		// Upgrade has already written an HTTP error response for a failed handshake,
		// so there is nothing left here to reply with.
		logger.Error("failed to upgrade the terminal connection", zap.Error(err))
		return nil
	}
	defer conn.Close()
	container := ctx.Param("id")
	hr, err := service.MyService.Docker().CreateContainerShellSession(container, row, col)
	if err != nil {
		// The connection was hijacked by the upgrade above, so a JSON response here
		// is written into something nobody reads: the browser saw a socket open and
		// close with nothing in it. That is the terminal that comes up blank and says
		// nothing -- for an image with no shell, a container that is not running, or
		// any other exec failure. The socket is the only channel left, so use it.
		logger.Error("failed to open a shell in the container", zap.Error(err), zap.String("container", container))

		if writeErr := conn.WriteMessage(websocket.TextMessage, []byte("\r\n"+err.Error()+"\r\n")); writeErr != nil {
			logger.Error("failed to report the shell error over the terminal connection", zap.Error(writeErr))
		}

		return nil
	}
	// 关闭I/O流
	defer hr.Close()
	// 退出进程
	defer func() {
		if _, err := hr.Conn.Write([]byte("exit\r")); err != nil {
			logger.Error("error when trying `exit` to container", zap.Error(err))
		}
	}()
	go func() {
		ssh.WsWriterCopy(hr.Conn, conn)
	}()
	ssh.WsReaderCopy(conn, hr.Conn)
	return nil
}

// @Summary 卸载app
// @Produce  application/json
// @Accept multipart/form-data
// @Tags app
// @Param  id path string true "容器id"
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/uninstall/{id} [delete]
func UninstallApp(ctx echo.Context) error {
	containerID := ctx.Param("id")
	if len(containerID) == 0 {
		return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	j := make(map[string]bool)
	if err := (&echo.DefaultBinder{}).BindBody(ctx, &j); err != nil {
		return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: err.Error()})
	}

	isDelete, ok := j["delete_config_folder"]
	if !ok {
		isDelete = false
	}

	httpCtx := common.WithProperties(context.Background(), v2.PropertiesFromQueryParams(ctx))

	container, err := service.MyService.Docker().DescribeContainer(httpCtx, containerID)
	if err != nil {
		if _, ok := err.(errdefs.ErrNotFound); ok {
			return ctx.JSON(http.StatusNotFound, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	common.SetProperties(httpCtx, map[string]string{
		common.PropertyTypeAppName.Name: v1.AppName(container),
		common.PropertyTypeAppIcon.Name: v1.AppIcon(container),
	})

	go func() {
		go service.PublishEventWrapper(httpCtx, common.EventTypeAppUninstallBegin, nil)

		defer service.PublishEventWrapper(httpCtx, common.EventTypeAppUninstallEnd, nil)

		if err := uninstall(httpCtx, container, isDelete); err != nil {
			go service.PublishEventWrapper(httpCtx, common.EventTypeAppUninstallError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
		}
	}()

	return ctx.JSON(http.StatusOK, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary 更新设置
// @Produce  application/json
// @Accept multipart/form-data
// @Tags app
// @Param  id path string true "容器id"
// @Param  shares formData string false "cpu权重"
// @Param  mem formData string false "内存大小MB"
// @Param  restart formData string false "重启策略"
// @Param  label formData string false "应用名称"
// @Param  position formData bool true "是否放到首页"
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/update/{id}/setting [put]
func UpdateSetting(ctx echo.Context) error {
	id := ctx.Param("id")
	if len(id) == 0 {
		return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: "id should not be empty"})
	}

	m := model.CustomizationPostData{}
	if err := ctx.Bind(&m); err != nil {
		return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: err.Error()})
	}

	if err := m.ValidateCustomLabels(); err != nil {
		return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: err.Error()})
	}

	if err := service.MyService.Docker().StopContainer(id); err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	for _, u := range m.Ports {
		protocol := strings.ToLower(u.Protocol)

		if !lo.Contains([]string{"tcp", "udp", "both"}, protocol) {
			return ctx.JSON(http.StatusBadRequest, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: "protocol should be tcp, udp or both"})
		}
	}

	if err := service.MyService.Docker().RenameContainer(id, id); err != nil {
		logger.Error("rename container error: ", zap.Error(err))
	}

	containerID, err := service.MyService.Docker().CreateContainer(m, id)
	if err != nil {
		if err := service.MyService.Docker().RenameContainer(m.ContainerName, id); err != nil {
			logger.Error("rename container error: ", zap.Error(err))
		}

		if err := service.MyService.Docker().StartContainer(id); err != nil {
			return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	// step：启动容器
	if err = service.MyService.Docker().StartContainer(containerID); err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	if err := service.MyService.Docker().RemoveContainer(id, true); err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(http.StatusOK, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

func GetDockerNetworks(ctx echo.Context) error {
	networks := service.MyService.Docker().GetNetworkList()
	list := []map[string]string{}
	for _, network := range networks {
		if network.Driver != "null" {
			list = append(list, map[string]string{"name": network.Name, "driver": network.Driver, "id": network.ID})
		}
	}

	return ctx.JSON(http.StatusOK, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: list})
}

func ToComposeYAML(ctx echo.Context) error {
	appID := ctx.Param("id")

	httpCtx := common.WithProperties(context.Background(), v2.PropertiesFromQueryParams(ctx))

	info, err := service.MyService.Docker().DescribeContainer(httpCtx, appID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	m := v1.GetCustomizationPostData(*info)

	return YAML(ctx, http.StatusOK, m.Compose())
}

// @Produce  application/json
// @Accept application/json
// @Tags app
// @Param  id path string true "appid"
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/update/{id}/info [get]
func ContainerUpdateInfo(ctx echo.Context) error {
	appID := ctx.Param("id")

	httpCtx := common.WithProperties(context.Background(), v2.PropertiesFromQueryParams(ctx))

	info, err := service.MyService.Docker().DescribeContainer(httpCtx, appID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	m := v1.GetCustomizationPostData(*info)

	return ctx.JSON(http.StatusOK, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: m})
}

// NOTE: the API is a temporary and internal API. It will be deleted in the future.
// the API is for archive v1 app for rebuilt v2 app.
func ArchiveContainer(ctx echo.Context) error {
	appID := ctx.Param("id")

	if err := service.MyService.Docker().StopContainer(appID); err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	// get container name
	container, err := service.MyService.Docker().GetContainer(appID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	// GetContainer reports "not found" as a zero value with a nil error, so an unknown
	// appID arrives here with no names at all and used to panic on Names[0].
	if len(container.Names) == 0 {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: fmt.Sprintf("container `%s` not found", appID)})
	}

	if err := service.MyService.Docker().RenameContainer(container.Names[0]+"_old", appID); err != nil {
		return ctx.JSON(http.StatusInternalServerError, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(http.StatusOK, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary my app hardware usage list
// @Produce  application/json
// @Accept application/json
// @Tags app
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/usage [get]
func AppUsageList(ctx echo.Context) error {
	list := service.MyService.Docker().GetContainerStats()
	return ctx.JSON(common_err.SUCCESS, &modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: list})
	// return ctx.JSON(common_err.SUCCESS, &modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: nil})
}

func uninstall(ctx context.Context, container *types.ContainerJSON, isDelete bool) error {
	// step：停止容器
	if err := func() error {
		go service.PublishEventWrapper(ctx, common.EventTypeContainerStopBegin, nil)

		defer service.PublishEventWrapper(ctx, common.EventTypeContainerStopEnd, nil)

		if err := service.MyService.Docker().StopContainer(container.ID); err != nil {

			go service.PublishEventWrapper(ctx, common.EventTypeContainerStopError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			return err
		}

		return nil
	}(); err != nil {
		return err
	}

	if err := func() error {
		go service.PublishEventWrapper(ctx, common.EventTypeContainerRemoveBegin, nil)

		defer service.PublishEventWrapper(ctx, common.EventTypeContainerRemoveEnd, nil)

		if err := service.MyService.Docker().RemoveContainer(container.ID, false); err != nil {
			go service.PublishEventWrapper(ctx, common.EventTypeContainerRemoveError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
			return err
		}

		return nil
	}(); err != nil {
		return err
	}

	if err := func() error {
		go service.PublishEventWrapper(ctx, common.EventTypeImageRemoveBegin, nil)

		defer service.PublishEventWrapper(ctx, common.EventTypeImageRemoveEnd, nil)

		if err := service.MyService.Docker().RemoveImage(container.Config.Image); err != nil {
			logger.Error("error when trying to remove docker image", zap.Error(err), zap.String("image", container.Config.Image))

			go service.PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
		}

		return nil
	}(); err != nil {
		return err
	}

	if container.Config.Labels["origin"] != "custom" && isDelete {
		// step: 删除文件夹
		for _, v := range container.Mounts {
			// Docker names a container "/nginx"; only <anywhere>/AppData/nginx is its to delete
			if path := service.AppDataFolder(v.Source, strings.TrimPrefix(container.Name, "/")); path != "" {
				if err := file.RMDir(path); err != nil {
					return err
				}
			}
		}
	}
	config.CasaOSGlobalVariables.AppChange = true

	return nil
}
