package v2

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
)

func dashboardHiddenOK(ctx echo.Context, app string) error {
	hidden := slices.Contains(service.DashboardHidden.List(), app)

	return ctx.JSON(http.StatusOK, codegen.DashboardHiddenOK{Data: &codegen.DashboardHidden{Hidden: hidden}})
}

// GetDashboardHidden tells whether the dashboard leaves the app's icon out (owner only).
func (a *AppManagement) GetDashboardHidden(ctx echo.Context, app codegen.VisibilityApp) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	return dashboardHiddenOK(ctx, app)
}

// SetDashboardHidden hides the app's icon from every account's dashboard, or shows it
// again (owner only). It changes nothing else: the app stays known to every list.
func (a *AppManagement) SetDashboardHidden(ctx echo.Context, app codegen.VisibilityApp) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	var body struct {
		Hidden *bool `json:"hidden"`
	}
	if !validAppName.MatchString(app) || json.NewDecoder(ctx.Request().Body).Decode(&body) != nil || body.Hidden == nil {
		message := "send JSON with hidden, true or false, for an app name of lowercase letters, digits, `-` and `_`"

		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if err := service.DashboardHidden.Set(app, *body.Hidden); err != nil {
		message := err.Error()

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return dashboardHiddenOK(ctx, app)
}
