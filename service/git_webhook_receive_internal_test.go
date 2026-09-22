package service

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
)

// freshGitWebhookRuns forgets what earlier tests' webhooks started: the debounce and the
// queued checks are kept per app name, and every test's app is jarvis.
func freshGitWebhookRuns(t *testing.T) {
	t.Helper()

	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()
	gitWebhookRuns.last, gitWebhookRuns.pending = map[string]time.Time{}, map[string]bool{}
}

// deliver sends body to jarvis's webhook with header.
func deliver(header http.Header, body []byte) (string, error) {
	return ReceiveGitWebhook("jarvis", header, bytes.NewReader(body))
}

// waitForGitCheckAfter waits until a check that started after since has ended, and
// whatever it started too, and returns the app's state.
func waitForGitCheckAfter(t *testing.T, name string, since time.Time) *gitApp {
	t.Helper()

	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if st, err := loadGitApp(name); err == nil && st.Check != nil && st.Check.At.After(since) && st.Operation == nil {
			return waitForGitApp(t, name)
		}
	}
	t.Fatal("no check ran")

	return nil
}

func lastDelivery(t *testing.T) *GitWebhookDelivery {
	t.Helper()

	view, err := GetGitApp(context.Background(), "jarvis")
	assert.NilError(t, err)
	assert.Assert(t, view.Webhook.LastDelivery != nil)

	return view.Webhook.LastDelivery
}

// The route does not tell which apps exist: an app that is not there, a name that is no
// app's, and an app whose webhook is off get the same refusal, and nothing is written.
func TestAWebhookIsRefusedAlikeForAnUnknownAppAndOneTurnedOff(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	freshGitWebhookRuns(t)
	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)
	body := []byte(`{"ref":"refs/heads/main"}`)
	header := signedDelivery(strings.Repeat("5e", 32), "push", body)

	_, unknown := ReceiveGitWebhook("nextcloud", header, bytes.NewReader(body))
	_, notAName := ReceiveGitWebhook("../jarvis", header, bytes.NewReader(body))
	_, off := deliver(header, body)

	assert.ErrorIs(t, unknown, ErrGitWebhookNotFound)
	assert.Equal(t, notAName, unknown)
	assert.Equal(t, off, unknown)
	for _, app := range []string{"jarvis", "nextcloud"} {
		_, err := os.Stat(gitAppFile(app, ".webhook"))
		assert.Assert(t, os.IsNotExist(err), "%s: nothing written", app)
	}
}

func TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	freshGitWebhookRuns(t)
	ctx := context.Background()
	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)
	secret := enableTestWebhook(t)
	body := []byte(`{"zen":"Design for failure."}`)

	answer, err := deliver(signedDelivery(secret, "ping", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookPong)
	delivery := lastDelivery(t)
	assert.Equal(t, delivery.Forge, "github")
	assert.Equal(t, delivery.Event, "ping")
	assert.Equal(t, delivery.Result, "pong")
	assert.Assert(t, time.Since(delivery.At) < time.Minute)

	answer, err = deliver(signedDelivery(secret, "issues", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookIgnored)
	assert.Equal(t, lastDelivery(t).Result, "ignored")

	// 5 MiB exactly is read and signed; one byte more is refused
	largest := bytes.Repeat([]byte(" "), gitWebhookMaxBody)
	answer, err = deliver(signedDelivery(secret, "issues", largest), largest)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookIgnored)

	// nothing a refused request sends is written down
	kept, err := os.ReadFile(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	tooLarge := append(largest, ' ')
	_, err = deliver(signedDelivery(secret, "push", tooLarge), tooLarge)
	assert.ErrorIs(t, err, ErrGitWebhookTooLarge)
	_, err = deliver(signedDelivery(strings.Repeat("0f", 32), "push", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(webhookHeader("X-GitHub-Event", "push"), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature, "unsigned")
	_, err = deliver(webhookHeader("X-Gitlab-Event", "Push Hook", "X-Gitlab-Token", strings.Repeat("0f", 32)), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	now, err := os.ReadFile(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	assert.Equal(t, string(now), string(kept))

	// a new secret refuses the old one at once
	regenerate := true
	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assert.NilError(t, err)
	_, err = deliver(signedDelivery(secret, "ping", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	answer, err = deliver(signedDelivery(view.Webhook.Secret, "ping", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookPong)

	// turned off, the app is one that does not exist
	off := false
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off})
	assert.NilError(t, err)
	_, err = deliver(signedDelivery(view.Webhook.Secret, "ping", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookNotFound)
}

// A push runs what "Check now" runs, the automatic deployment included; the pushes a forge
// sends within ten seconds of it run nothing more.
func TestAPushChecksTheAppOnceWithinTenSeconds(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	second := pushTestCommit(t, work, "index.html", "v2")
	assert.Equal(t, gitWebhookDebounce, 10*time.Second)
	previous := gitWebhookDebounce
	t.Cleanup(func() { gitWebhookDebounce = previous })
	// the window outlasts a slow runner's build
	gitWebhookDebounce = time.Hour

	answer, err := deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookChecking)
	st := waitForGitApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Deployed.Commit, second, "the automatic rebuild deploys what the check saw")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	delivery := lastDelivery(t)
	assert.Equal(t, delivery.Forge, "github")
	assert.Equal(t, delivery.Event, "push")
	assert.Equal(t, delivery.Result, "checked")

	checked := st.Check.At
	pushTestCommit(t, work, "index.html", "v3")
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookCoalesced)
	st = waitForGitApp(t, "jarvis")
	assert.Assert(t, st.Check.At.Equal(checked), "no second check")
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, lastDelivery(t).Result, "coalesced")

	// past the window, a push checks again
	gitWebhookDebounce = 0
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookChecking)
	assert.Assert(t, waitForGitApp(t, "jarvis").Check.At.After(checked))
}

// A push to an app something else holds gets one check, run once the app is free; with the
// automatic rebuild off, that check deploys nothing.
func TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	second := pushTestCommit(t, work, "index.html", "v2")
	before := waitForGitApp(t, "jarvis").Check.At

	end, err := Begin("jarvis", gitOperationDeploy)
	assert.NilError(t, err)
	answer, err := deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookQueued)
	assert.Equal(t, lastDelivery(t).Result, "queued")
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookCoalesced, "one check waits, not two")

	time.Sleep(2 * howOftenToAskAgain)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, st.Check.At.Equal(before), "nothing checks while the app is held")

	end()
	st = waitForGitCheckAfter(t, "jarvis", before)
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Deployed.Commit, first, "the automatic rebuild is off: nothing is deployed")
	assert.Equal(t, len(fake.Calls()), 0, "nothing is built")

	checked := st.Check.At
	time.Sleep(2 * howOftenToAskAgain)
	assert.Assert(t, waitForGitApp(t, "jarvis").Check.At.Equal(checked), "and only one check ran")
}

// syncBuffer is a log the checks running in the background write to while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestAWebhookLogsNeitherItsBodyNorItsSignatures(t *testing.T) {
	_, work, _ := deployedTestApp(t, false)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	pushTestCommit(t, work, "index.html", "v2")

	var out syncBuffer
	logger.LogInitWithWriterSyncers(zapcore.AddSync(&out))
	t.Cleanup(func() { logger.LogInitWithWriterSyncers(zapcore.AddSync(os.Stdout)) })

	body := []byte(`{"ref":"refs/heads/main","marker":"body-marker-7f3a"}`)
	good := signedDelivery(secret, "push", body)
	bad := signedDelivery(strings.Repeat("0f", 32), "push", body)
	tooLarge := append(bytes.Repeat([]byte(" "), gitWebhookMaxBody), body...)

	_, err := deliver(good, body)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
	_, err = deliver(bad, body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(webhookHeader("X-Gitlab-Event", "Push Hook", "X-Gitlab-Token", "token-marker-91c2"), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(signedDelivery(secret, "push", tooLarge), tooLarge)
	assert.ErrorIs(t, err, ErrGitWebhookTooLarge)

	log := out.String()
	for _, line := range []string{"checked", "refused: invalid signature", "refused: body over 5 MiB"} {
		assert.Assert(t, strings.Contains(log, line), "the log says %q:\n%s", line, log)
	}
	for name, value := range map[string]string{
		"the body":             "body-marker-7f3a",
		"the secret":           secret,
		"the right signature":  strings.TrimPrefix(good.Get("X-Hub-Signature-256"), "sha256="),
		"a wrong signature":    strings.TrimPrefix(bad.Get("X-Hub-Signature-256"), "sha256="),
		"a wrong gitlab token": "token-marker-91c2",
	} {
		assert.Assert(t, !strings.Contains(log, value), "%s is in the log", name)
	}
}
