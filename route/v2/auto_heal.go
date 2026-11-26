package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
)

// AutoHeal is whether an unhealthy app is restarted by itself.
func (a *AppManagement) AutoHeal(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	return ctx.JSON(http.StatusOK, codegen.AutoHealOK{Data: &codegen.AutoHeal{Enabled: service.AutoHealEnabled()}})
}

func (a *AppManagement) SetAutoHeal(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	// a pointer: a body that leaves `enabled` out must not read as "turn it off"
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := ctx.Bind(&body); err != nil || body.Enabled == nil {
		message := "enabled: true or false is required"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}
	if err := service.SetAutoHeal(*body.Enabled); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.AutoHealOK{Data: &codegen.AutoHeal{Enabled: *body.Enabled}})
}
