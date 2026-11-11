package v2

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"

	"github.com/labstack/echo/v4"
)

func (a *AppManagement) GetGlobalSettings(ctx echo.Context) error {
	result := make([]codegen.GlobalSetting, 0)
	for key, value := range config.Globals() {
		result = append(result, codegen.GlobalSetting{
			Key:         utils.Ptr(key),
			Value:       value,
			Description: utils.Ptr(key),
		})
	}

	return ctx.JSON(http.StatusOK, codegen.GlobalSettingsOK{
		Data: &result,
	})
}

func (a *AppManagement) GetGlobalSetting(ctx echo.Context, key codegen.GlobalSettingKey) error {
	value, ok := config.Globals()[string(key)]
	if !ok {
		message := "the key is not exist"
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.GlobalSettingOK{
		Data: &codegen.GlobalSetting{
			Key:         utils.Ptr(key),
			Value:       value,
			Description: utils.Ptr(key),
		},
	})
}

func (a *AppManagement) UpdateGlobalSetting(ctx echo.Context, key codegen.GlobalSettingKey) error {
	var action codegen.GlobalSetting
	if err := ctx.Bind(&action); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}
	if err := updateGlobalEnv(ctx, key, action.Value); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.GlobalSettingOK{
		Data: &codegen.GlobalSetting{
			Key:         utils.Ptr(key),
			Value:       action.Value,
			Description: utils.Ptr(key),
		},
	})
}

func updateGlobalEnv(ctx echo.Context, key string, value string) error {
	if key == "" {
		return fmt.Errorf("openai api key is required")
	}
	if err := service.MyService.AppStoreManagement().ChangeGlobal(key, value); err != nil {
		return err
	}

	reapplyGlobals(PropertiesFromQueryParams(ctx))

	return nil
}

func deleteGlobalEnv(ctx echo.Context, key string) error {

	if err := service.MyService.AppStoreManagement().DeleteGlobal(key); err != nil {
		return err
	}

	reapplyGlobals(PropertiesFromQueryParams(ctx))

	return nil
}

// reapplyGlobals gives every app the global variables as they are now, in the background. The
// properties are read by the caller: echo takes its context back when the handler returns.
func reapplyGlobals(properties map[string]string) {
	go func() {
		backgroundCtx := common.WithProperties(context.Background(), properties)
		apps, err := service.MyService.Compose().List(backgroundCtx)
		if err != nil {
			logger.Error("failed to list the apps to give the global variables to", zap.Error(err))
			return
		}
		compose, dockerClient, err := service.ApiService()
		if err != nil {
			logger.Error("failed to get the compose service", zap.Error(err))
			return
		}
		defer dockerClient.Close()

		for name, app := range apps {
			if err := app.ReapplyGlobals(backgroundCtx, compose); err != nil {
				logger.Error("failed to give an app the global variables", zap.Error(err), zap.String("name", name))
			}
		}
	}()
}

func (a *AppManagement) DeleteGlobalSetting(ctx echo.Context, key codegen.GlobalSettingKey) error {
	var action codegen.GlobalSetting
	if err := ctx.Bind(&action); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if err := deleteGlobalEnv(ctx, key); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{})
}
