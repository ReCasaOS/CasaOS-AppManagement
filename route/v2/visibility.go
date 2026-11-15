package v2

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/errdefs"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
)

func isInternal(ctx echo.Context) bool {
	return external.IsInternalRequest(ctx.RealIP(), ctx.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
}

// isOwner: the box's own services and the admin account. A request with no user_id that
// is not internal is nobody.
func isOwner(ctx echo.Context) bool {
	if isInternal(ctx) {
		return true
	}
	id, err := strconv.Atoi(ctx.Request().Header.Get("user_id"))

	return err == nil && external.RoleOf(config.CommonInfo.RuntimePath, id) == "admin"
}

// callerID is the account id the token check set; false when there is none to read.
func callerID(ctx echo.Context) (int, bool) {
	id, err := strconv.Atoi(ctx.Request().Header.Get("user_id"))

	return id, err == nil
}

// visibleFilter says whether an app is visible to the caller. It reads the file once, so
// a list asks it for every app and pays for one parse. Internal requests and the admin see
// everything; a caller no account can be told from sees no app.
func visibleFilter(ctx echo.Context) func(app string) bool {
	if isOwner(ctx) {
		return func(string) bool { return true }
	}
	id, known := callerID(ctx)
	if !known {
		return func(string) bool { return false }
	}
	snapshot := service.AppVisibility.Snapshot()

	return func(app string) bool { return !service.HiddenIn(snapshot, app, id) }
}

// visibleApps is the caller's share of the installed apps.
func visibleApps(ctx echo.Context, apps map[string]*service.ComposeApp) map[string]*service.ComposeApp {
	visible := visibleFilter(ctx)

	return lo.PickBy(apps, func(id string, _ *service.ComposeApp) bool { return visible(id) })
}

// visibleTo is false when app is hidden from the account making the request.
func visibleTo(ctx echo.Context, app string) bool {
	return visibleFilter(ctx)(app)
}

// hiddenApps lists the apps to leave out of an answer that cannot be filtered one app at
// a time (the logs). ok is false for a caller no account can be told from: nothing can be
// left out for them, so the route refuses.
func hiddenApps(ctx echo.Context) (apps []string, ok bool) {
	if isOwner(ctx) {
		return nil, true
	}
	id, known := callerID(ctx)
	if !known {
		return nil, false
	}
	for app, accounts := range service.AppVisibility.Snapshot() {
		if slices.Contains(accounts, id) {
			apps = append(apps, app)
		}
	}

	return apps, true
}

// ContainerHidden is true when the container belongs to a compose project hidden from the
// caller: its id must not read what the project's own routes would not.
func ContainerHidden(ctx echo.Context, inspected *types.ContainerJSON) bool {
	project := composeProjectOf(inspected)

	return project != "" && !visibleTo(ctx, project)
}

// VisibleFilter is visibleFilter for the v1 routes.
func VisibleFilter(ctx echo.Context) func(app string) bool { return visibleFilter(ctx) }

// IsOwner is isOwner for the v1 routes.
func IsOwner(ctx echo.Context) bool { return isOwner(ctx) }

// HiddenApps is hiddenApps for the v1 routes.
func HiddenApps(ctx echo.Context) ([]string, bool) { return hiddenApps(ctx) }

// probeID is a container id no daemon has.
const probeID = "0000000000000000000000000000000000000000000000000000000000000000"

// probeApp is an app name no visibility entry can hold (longer than validAppName allows),
// yet one every service reads as a well-formed name: a hidden app is asked about under it,
// so the answer, errors included, is the one for an app that does not exist.
const probeApp = "probe-0000000000000000000000000000000000000000000000000000000000000000"

// askAs is the name to ask the services about for app: probeApp when it is hidden, inside
// the caller's own surrounding whitespace, so a service that trims the name and one that
// echoes it both answer as for an unknown name. A service may trim the name (a backup's
// folder) or read it on a remote that ignores case, so the trimmed, lowercased name is
// checked too.
func askAs(ctx echo.Context, app string) string {
	visible := visibleFilter(ctx)
	trimmed := strings.TrimSpace(app)
	if visible(app) && visible(strings.ToLower(trimmed)) {
		return app
	}
	lead := app[:strings.Index(app, trimmed)]

	return lead + probeApp + app[len(lead)+len(trimmed):]
}

// asked puts the caller's name back where the services named probeApp; the whitespace around
// the probe is the caller's already.
func asked(app string, err error) error {
	if err == nil || !strings.Contains(err.Error(), probeApp) {
		return err
	}

	return renamedError{err, strings.ReplaceAll(err.Error(), probeApp, strings.TrimSpace(app))}
}

type renamedError struct {
	error
	text string
}

func (e renamedError) Error() string { return e.text }
func (e renamedError) Unwrap() error { return e.error }

// ContainerAsk is what to ask the daemon about a container for a caller. A container of an
// app hidden from the caller is asked about as probeID, so every answer, its errors
// included, is the daemon's own for a container that does not exist.
type ContainerAsk struct{ Ref, id string }

// AskAboutContainer decides what to ask the daemon for id. For a caller that is not the
// owner, a container whose app cannot be checked (the daemon failed the first question for
// another reason than "no such container") is asked about as probeID too.
func AskAboutContainer(ctx echo.Context, id string) ContainerAsk {
	if !isOwner(ctx) {
		inspected, err := service.MyService.Docker().DescribeContainer(ctx.Request().Context(), id)
		if err != nil && !errdefs.IsNotFound(err) || err == nil && ContainerHidden(ctx, inspected) {
			return ContainerAsk{Ref: probeID, id: id}
		}
	}

	return ContainerAsk{Ref: id, id: id}
}

// Words puts the caller's id back into what the daemon said about Ref.
func (a ContainerAsk) Words(text string) string { return strings.ReplaceAll(text, a.Ref, a.id) }

func forbidden(ctx echo.Context) error {
	message := "only the owner can do this"

	return ctx.JSON(http.StatusForbidden, codegen.ResponseForbidden{Message: &message})
}

func visibilityOK(ctx echo.Context, app string) error {
	hidden := service.AppVisibility.Get(app)

	return ctx.JSON(http.StatusOK, codegen.VisibilityOK{Data: &codegen.Visibility{HiddenFor: hidden}})
}

// GetVisibility lists the accounts app is hidden from (owner only).
func (a *AppManagement) GetVisibility(ctx echo.Context, app codegen.VisibilityApp) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	return visibilityOK(ctx, app)
}

// SetVisibility replaces them (owner only). The body is read strictly: a misspelt field
// would otherwise show the app to everyone again.
func (a *AppManagement) SetVisibility(ctx echo.Context, app codegen.VisibilityApp) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	if !validAppName.MatchString(app) {
		message := "an app name is lowercase letters, digits, `-` and `_`, at most 64, starting with a letter or a digit"

		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	var body struct {
		HiddenFor *[]int `json:"hidden_for"`
	}
	decoder := json.NewDecoder(ctx.Request().Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&body)
	if err == nil && decoder.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err != nil || body.HiddenFor == nil {
		message := "visibility is JSON with hidden_for, a list of account ids"

		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if err := service.AppVisibility.Hide(app, *body.HiddenFor); err != nil {
		message := err.Error()
		if errors.Is(err, service.ErrBadAccountID) {
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return visibilityOK(ctx, app)
}

// validAppName is a compose project name as an entry may hold it.
var validAppName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ListVisibilityOrphans lists the entries no installed app has (owner only). An uninstall
// keeps an entry, so that what stays of the app (logs, stats, backups) stays hidden.
func (a *AppManagement) ListVisibilityOrphans(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	installed, err := service.MyService.Compose().List(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	gitApps, err := service.GitAppNames()
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	orphans := []string{}
	for app := range service.AppVisibility.Snapshot() {
		if _, ok := installed[app]; !ok && !slices.Contains(gitApps, app) {
			orphans = append(orphans, app)
		}
	}
	slices.Sort(orphans)

	return ctx.JSON(http.StatusOK, codegen.VisibilityOrphansOK{Data: &orphans})
}

// ForgetVisibility removes an app's entry (owner only): the app is shown to everyone.
func (a *AppManagement) ForgetVisibility(ctx echo.Context, app codegen.VisibilityApp) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	if err := service.AppVisibility.RemoveApp(app); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	message := "OK"

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: &message})
}

// ForgetVisibilityAccount is for user-service when it deletes an account (internal only).
// An id nothing mentions is answered the same way.
func (a *AppManagement) ForgetVisibilityAccount(ctx echo.Context, id int) error {
	if !isInternal(ctx) {
		return forbidden(ctx)
	}

	if err := service.AppVisibility.RemoveAccount(id); err != nil {
		message := err.Error()

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	message := "OK"

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: &message})
}
