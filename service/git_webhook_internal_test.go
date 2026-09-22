package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

var webhookSecretPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// enableTestWebhook turns jarvis's webhook on and returns its secret.
func enableTestWebhook(t *testing.T) string {
	t.Helper()

	on := true
	view, err := UpdateGitApp(context.Background(), "jarvis", GitAppChanges{WebhookEnabled: &on})
	assert.NilError(t, err)
	assert.Equal(t, view.Webhook.Enabled, true)

	return view.Webhook.Secret
}

func TestAWebhookSecretIsMadeReplacedAndForgotten(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	path := "/v2/app_management/git/jarvis/webhook"
	off, regenerate := false, true

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Path: path})
	shown, err := stdjson.Marshal(view.Webhook)
	assert.NilError(t, err)
	assert.Equal(t, string(shown), `{"enabled":false,"path":"`+path+`","last_delivery":null}`, "no secret while off")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assertBadRequest(t, err, "the webhook is off")

	first := enableTestWebhook(t)
	assert.Assert(t, webhookSecretPattern.MatchString(first), first)
	info, err := os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Enabled: true, Path: path, Secret: first})
	assert.Equal(t, enableTestWebhook(t), first, "turning it on again keeps the secret")

	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assert.NilError(t, err)
	second := view.Webhook.Secret
	assert.Assert(t, webhookSecretPattern.MatchString(second), second)
	assert.Assert(t, second != first, "regenerating replaces the secret")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off, RegenerateWebhookSecret: &regenerate})
	assertBadRequest(t, err, "the webhook is off")
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Webhook.Secret, second, "a refused change changes nothing")

	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off})
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Path: path})
	_, err = os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.Assert(t, os.IsNotExist(err), "turning it off forgets the secret")

	third := enableTestWebhook(t)
	assert.Assert(t, third != first && third != second, "turning it on again after off makes a new secret")

	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	_, err = os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.Assert(t, os.IsNotExist(err), "the secret goes with the app")
}

// A backup holds no secret: whatever this machine kept, the restored app's webhook is off,
// whether the restore registers the app or finds it registered already.
func TestARestoredGitAppComesBackWithItsWebhookOff(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	origin := BackupGit{Remote: url, Branch: "main", Commit: gitShell(t, work, "git rev-parse HEAD")}
	off := GitWebhookView{Path: "/v2/app_management/git/jarvis/webhook"}
	// a secret without a state, as an uninstall that could not remove everything leaves it
	assert.NilError(t, saveGitWebhook("jarvis", &gitWebhook{Secret: strings.Repeat("ab", 32)}))

	_, err := installGitAppFromBackup(ctx, "jarvis", origin, nil)
	assert.NilError(t, err)
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, off)

	// registered already, as a restore that asked for access leaves it, its webhook turned on
	enableTestWebhook(t)
	_, err = installGitAppFromBackup(ctx, "jarvis", origin, nil)
	assert.NilError(t, err)
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, off)
}
