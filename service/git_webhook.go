package service

import (
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// A git app's webhook: its secret and its last delivery, in `<app>.webhook` beside the
// app's state, readable by root only. The file exists while the webhook is on.
//
// Not in the state file: whatever holds the app (a check, a deployment) rewrites that one
// whole from its own copy, and would drop a delivery written meanwhile, or lose what it
// wrote itself under a delivery written from an older copy.

// GitWebhookView is the `webhook` of a git app in the API.
type GitWebhookView struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
	// Secret is set while the webhook is on.
	Secret       string              `json:"secret,omitempty"`
	LastDelivery *GitWebhookDelivery `json:"last_delivery"`
}

// GitWebhookDelivery is the last delivery whose signature was valid.
type GitWebhookDelivery struct {
	At     time.Time `json:"at"`
	Forge  string    `json:"forge"`
	Event  string    `json:"event"`
	Result string    `json:"result"`
}

type gitWebhook struct {
	Secret       string              `json:"secret"`
	LastDelivery *GitWebhookDelivery `json:"last_delivery"`
}

// gitWebhooks serialises what reads and writes the webhook files: a change from the API and
// a delivery.
//
// ponytail: one lock for every app; per-app locks if deliveries to many apps ever queue
// behind it.
var gitWebhooks sync.Mutex

// gitWebhookPath is the route a forge is given for app, after the address it reaches the
// box by.
func gitWebhookPath(app string) string {
	return "/v2/app_management/git/" + app + "/webhook"
}

// loadGitWebhook is app's webhook, nil while it is off.
func loadGitWebhook(app string) (*gitWebhook, error) {
	buf, err := os.ReadFile(gitAppFile(app, ".webhook"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var hook gitWebhook
	if err := stdjson.Unmarshal(buf, &hook); err != nil {
		return nil, fmt.Errorf("%s: %w", gitAppFile(app, ".webhook"), err)
	}

	return &hook, nil
}

func saveGitWebhook(app string, hook *gitWebhook) error {
	buf, err := stdjson.Marshal(hook)
	if err != nil {
		return err
	}

	return writeGitFile(gitAppFile(app, ".webhook"), buf)
}

// setGitWebhook turns app's webhook on or off as enabled says, nil leaving it as it is, and
// replaces its secret when regenerate is set. Turning it on makes a secret when there is
// none; turning it off forgets the secret, and the last delivery with it.
func setGitWebhook(app string, enabled *bool, regenerate bool) error {
	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	if enabled != nil && !*enabled {
		if err := os.Remove(gitAppFile(app, ".webhook")); err != nil && !os.IsNotExist(err) {
			return err
		}

		return nil
	}

	hook, err := loadGitWebhook(app)
	if err != nil {
		return err
	}

	switch {
	case hook == nil && enabled == nil, hook != nil && !regenerate:
		return nil
	case hook == nil:
		hook = &gitWebhook{}
	}

	secret := make([]byte, 32)
	_, _ = rand.Read(secret) // never fails since Go 1.24: it crashes instead
	hook.Secret = hex.EncodeToString(secret)

	return saveGitWebhook(app, hook)
}

// gitWebhookView is what the API shows of app's webhook.
func gitWebhookView(app string) GitWebhookView {
	view := GitWebhookView{Path: gitWebhookPath(app)}
	if hook, err := loadGitWebhook(app); err == nil && hook != nil {
		view.Enabled, view.Secret, view.LastDelivery = true, hook.Secret, hook.LastDelivery
	}

	return view
}
