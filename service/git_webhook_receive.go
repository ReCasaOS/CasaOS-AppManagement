package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/loader"
	"go.uber.org/zap"
)

// A forge telling CasaOS that an app's branch moved: the app is checked at once, the way
// "Check now" checks it, instead of at the next poll.

// Refusals of a delivery, answered 404, 401 and 413.
var (
	// ErrGitWebhookNotFound is an unknown app and a webhook turned off alike, so that the
	// route does not tell which apps exist.
	ErrGitWebhookNotFound  = errors.New("not found")
	ErrGitWebhookSignature = errors.New("invalid signature")
	ErrGitWebhookTooLarge  = errors.New("the body is over 5 MiB")
)

const (
	// gitWebhookMaxBody is as much body as is read and signed.
	gitWebhookMaxBody = 5 << 20
	// gitWebhookPatience is how long a check a webhook queued waits for a busy app.
	gitWebhookPatience = 30 * time.Minute
)

// gitWebhookDebounce is the least time between two checks webhooks start for one app: a
// forge may send several events for one push. A var, so tests can shorten it.
var gitWebhookDebounce = 10 * time.Second

// gitWebhookRuns is what webhooks started, guarded by gitWebhooks: when a webhook last
// started a check of each app, and the apps a queued check waits for.
var gitWebhookRuns = struct {
	last    map[string]time.Time
	pending map[string]bool
}{last: map[string]time.Time{}, pending: map[string]bool{}}

// ReceiveGitWebhook is a forge's delivery for app: the answer the forge is given, or one
// of the refusals. A refused delivery writes nothing, and no body, no header's value and
// no signature reaches the log.
func ReceiveGitWebhook(app string, header http.Header, body io.Reader) (string, error) {
	// a name from a request: an app's name or nothing, never a path
	if app == "" || app != loader.NormalizeProjectName(app) {
		return "", ErrGitWebhookNotFound
	}

	forge, event, action := gitWebhookEvent(header)
	log := func(outcome string) {
		logger.Info("git webhook", zap.String("app", app), zap.String("forge", forge), zap.String("event", event), zap.String("outcome", outcome))
	}

	payload, err := io.ReadAll(io.LimitReader(body, gitWebhookMaxBody+1))
	if err != nil {
		logger.Error("the body of a git webhook could not be read", zap.String("app", app), zap.Error(err))
		return "", err
	}
	if len(payload) > gitWebhookMaxBody {
		log("refused: body over 5 MiB")
		return "", ErrGitWebhookTooLarge
	}

	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	hook, err := loadGitWebhook(app)
	if err != nil {
		logger.Error("a git webhook could not be read", zap.String("app", app), zap.Error(err))
		return "", err
	}
	if hook == nil || !pathExists(gitAppFile(app, ".json")) {
		return "", ErrGitWebhookNotFound
	}
	if !gitWebhookSigned(header, payload, hook.Secret) {
		log("refused: invalid signature")
		return "", ErrGitWebhookSignature
	}

	answer := action
	if action == gitWebhookCheck {
		if answer, err = scheduleGitWebhookCheck(app); err != nil {
			logger.Error("the check a git webhook asked for could not start", zap.String("app", app), zap.Error(err))
			return "", err
		}
	}

	result := answer
	if answer == GitWebhookChecking {
		result = "checked"
	}
	hook.LastDelivery = &GitWebhookDelivery{At: time.Now().UTC(), Forge: forge, Event: event, Result: result}
	if err := saveGitWebhook(app, hook); err != nil {
		logger.Error("the last delivery of a git webhook could not be written down", zap.String("app", app), zap.Error(err))
	}
	log(result)

	return answer, nil
}

// scheduleGitWebhookCheck starts the check a push asks for, gitWebhooks held: at once when
// the app is free, once it is when it is busy, and not at all when a webhook started one
// less than gitWebhookDebounce ago or one waits already.
func scheduleGitWebhookCheck(app string) (string, error) {
	if gitWebhookRuns.pending[app] || time.Since(gitWebhookRuns.last[app]) < gitWebhookDebounce {
		return GitWebhookCoalesced, nil
	}

	st, end, err := beginGitCheck(context.Background(), app)
	if errors.As(err, new(ErrAppBusy)) {
		gitWebhookRuns.pending[app] = true
		go runQueuedGitWebhookCheck(app)

		return GitWebhookQueued, nil
	}
	if err != nil {
		return "", err
	}

	gitWebhookRuns.last[app] = time.Now()
	go runGitCheck(st, end)

	return GitWebhookChecking, nil
}

// runQueuedGitWebhookCheck waits for the busy app to come free, up to gitWebhookPatience,
// then checks it as the webhook would have.
func runQueuedGitWebhookCheck(app string) {
	ctx := context.Background()
	end, err := beginWaiting(ctx, app, gitOperationCheck, gitWebhookPatience)

	gitWebhooks.Lock()
	delete(gitWebhookRuns.pending, app)
	if err == nil {
		gitWebhookRuns.last[app] = time.Now()
	}
	gitWebhooks.Unlock()

	if err != nil {
		logger.Info("a check a git webhook queued gave up waiting", zap.String("app", app), zap.Error(err))
		return
	}

	st, err := prepareGitCheck(ctx, app)
	if err != nil {
		end()
		logger.Error("a check a git webhook queued could not start", zap.String("app", app), zap.Error(err))

		return
	}

	runGitCheck(st, end)
}
