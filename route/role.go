package route

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/labstack/echo/v4"
)

// rejectWritesFromUsers makes the `user` role read-only, as the Gateway already does
// before a request gets here. It runs after the token is checked, which sets user_id.
// The terminal is a GET but a shell, so it counts as a write. Requests from the box's
// own services carry no user_id and pass; so does the one route that has no token (a
// git forge's delivery), which proves itself with its own secret.
func rejectWritesFromUsers() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if (req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodOptions) && !strings.HasSuffix(c.Path(), "/terminal") {
				return next(c)
			}
			if external.IsInternalRequest(c.RealIP(), req.Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath) {
				return next(c)
			}
			raw := req.Header.Get("user_id")
			if raw == "" {
				return next(c)
			}
			// an id that is not a number is no admin
			if id, err := strconv.Atoi(raw); err == nil && external.RoleOf(config.CommonInfo.RuntimePath, id) == "admin" {
				return next(c)
			}

			return c.JSON(http.StatusForbidden, map[string]string{"message": "this account can look but not change"})
		}
	}
}
