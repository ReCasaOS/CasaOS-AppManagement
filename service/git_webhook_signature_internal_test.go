package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// webhookHeader is a delivery's headers, given as name, value, name, value...
func webhookHeader(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Add(pairs[i], pairs[i+1])
	}

	return header
}

// webhookHMAC is the hex HMAC-SHA256 of body with secret, what the forges send.
func webhookHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

// signedDelivery is the headers GitHub sends with event, signed with secret.
func signedDelivery(secret, event string, body []byte) http.Header {
	return webhookHeader("X-GitHub-Event", event, "X-Hub-Signature-256", "sha256="+webhookHMAC(secret, body))
}

func TestAWebhookIsSignedOnlyWithTheAppsSecret(t *testing.T) {
	secret := strings.Repeat("5e", 32)
	other := strings.Repeat("0f", 32)
	body := []byte(`{"ref":"refs/heads/main"}`)
	good, wrong := webhookHMAC(secret, body), webhookHMAC(other, body)

	for _, c := range []struct {
		name   string
		header http.Header
		signed bool
	}{
		{"github, right secret", webhookHeader("X-Hub-Signature-256", "sha256="+good), true},
		{"github, upper-case hex", webhookHeader("X-Hub-Signature-256", "sha256="+strings.ToUpper(good)), true},
		{"github, wrong secret", webhookHeader("X-Hub-Signature-256", "sha256="+wrong), false},
		{"github, truncated", webhookHeader("X-Hub-Signature-256", "sha256="+good[:62]), false},
		{"github, odd length", webhookHeader("X-Hub-Signature-256", "sha256="+good[:63]), false},
		{"github, no prefix", webhookHeader("X-Hub-Signature-256", good), false},
		{"github, wrong prefix", webhookHeader("X-Hub-Signature-256", "sha1="+good), false},
		{"github, empty", webhookHeader("X-Hub-Signature-256", ""), false},
		{"gitea, right secret", webhookHeader("X-Gitea-Signature", good), true},
		{"gitea, wrong secret", webhookHeader("X-Gitea-Signature", wrong), false},
		{"gitea, truncated", webhookHeader("X-Gitea-Signature", good[:62]), false},
		{"gitea, with github's prefix", webhookHeader("X-Gitea-Signature", "sha256="+good), false},
		{"gitea, empty", webhookHeader("X-Gitea-Signature", ""), false},
		{"forgejo, right secret", webhookHeader("X-Forgejo-Signature", good), true},
		{"forgejo, wrong secret", webhookHeader("X-Forgejo-Signature", wrong), false},
		{"forgejo, truncated", webhookHeader("X-Forgejo-Signature", good[:62]), false},
		{"forgejo, with github's prefix", webhookHeader("X-Forgejo-Signature", "sha256="+good), false},
		{"forgejo, empty", webhookHeader("X-Forgejo-Signature", ""), false},
		{"gogs, right secret", webhookHeader("X-Gogs-Signature", good), true},
		{"gogs, wrong secret", webhookHeader("X-Gogs-Signature", wrong), false},
		{"gogs, truncated", webhookHeader("X-Gogs-Signature", good[:62]), false},
		{"gogs, with github's prefix", webhookHeader("X-Gogs-Signature", "sha256="+good), false},
		{"gogs, empty", webhookHeader("X-Gogs-Signature", ""), false},
		{"gitlab, right secret", webhookHeader("X-Gitlab-Token", secret), true},
		{"gitlab, wrong secret", webhookHeader("X-Gitlab-Token", other), false},
		{"gitlab, truncated", webhookHeader("X-Gitlab-Token", secret[:63]), false},
		{"gitlab, the HMAC instead of the secret", webhookHeader("X-Gitlab-Token", good), false},
		{"gitlab, empty", webhookHeader("X-Gitlab-Token", ""), false},
		{"no signature at all", webhookHeader("X-GitHub-Event", "push"), false},
		{"gitea's three headers, all right", webhookHeader("X-Gitea-Signature", good, "X-Gogs-Signature", good, "X-Hub-Signature-256", "sha256="+good), true},
		{"two contradicting headers", webhookHeader("X-Gitea-Signature", good, "X-Hub-Signature-256", "sha256="+wrong), false},
		{"two contradicting headers, the wrong one first", webhookHeader("X-Gitea-Signature", wrong, "X-Hub-Signature-256", "sha256="+good), false},
		{"a right signature and a wrong token", webhookHeader("X-Hub-Signature-256", "sha256="+good, "X-Gitlab-Token", other), false},
		{"one header twice, contradicting itself", webhookHeader("X-Hub-Signature-256", "sha256="+good, "X-Hub-Signature-256", "sha256="+wrong), false},
	} {
		assert.Equal(t, gitWebhookSigned(c.header, body, secret), c.signed, c.name)
		assert.Equal(t, gitWebhookSigned(c.header, body, ""), false, "%s: no secret signs nothing", c.name)
		if c.header.Get("X-Gitlab-Token") == "" {
			// GitLab sends the secret itself; every other forge signs the body
			assert.Equal(t, gitWebhookSigned(c.header, append([]byte(" "), body...), secret), false, "%s: another body", c.name)
		}
	}
}

func TestAWebhookNamesItsForgeAndItsEvent(t *testing.T) {
	long := strings.Repeat("x", 100)

	for _, c := range []struct {
		header               http.Header
		forge, event, action string
	}{
		{webhookHeader("X-GitHub-Event", "ping", "X-Hub-Signature-256", "sha256=00"), "github", "ping", GitWebhookPong},
		{webhookHeader("X-GitHub-Event", "push"), "github", "push", gitWebhookCheck},
		{webhookHeader("X-GitHub-Event", "issues"), "github", "issues", GitWebhookIgnored},
		{webhookHeader("X-Gitea-Event", "push", "X-Gogs-Event", "push", "X-GitHub-Event", "push"), "gitea", "push", gitWebhookCheck},
		{webhookHeader("X-Gitea-Event", "ping"), "gitea", "ping", GitWebhookIgnored},
		{webhookHeader("X-Forgejo-Event", "push", "X-Gitea-Event", "push", "X-Gogs-Event", "push"), "forgejo", "push", gitWebhookCheck},
		{webhookHeader("X-Forgejo-Event", "release"), "forgejo", "release", GitWebhookIgnored},
		{webhookHeader("X-Gogs-Event", "push"), "gogs", "push", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Push Hook"), "gitlab", "Push Hook", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Tag Push Hook"), "gitlab", "Tag Push Hook", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Merge Request Hook"), "gitlab", "Merge Request Hook", GitWebhookIgnored},
		{webhookHeader("X-Gitlab-Event", "push"), "gitlab", "push", GitWebhookIgnored},
		{webhookHeader("X-GitHub-Event", long), "github", long[:64], GitWebhookIgnored},
		{webhookHeader("X-Hub-Signature-256", "sha256=00"), "github", "", gitWebhookCheck},
		{webhookHeader("X-Gitea-Signature", "00", "X-Hub-Signature-256", "sha256=00"), "gitea", "", gitWebhookCheck},
		{webhookHeader("X-Forgejo-Signature", "00", "X-Gitea-Signature", "00"), "forgejo", "", gitWebhookCheck},
		{webhookHeader("X-Gogs-Signature", "00"), "gogs", "", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Token", "t"), "gitlab", "", gitWebhookCheck},
		{webhookHeader(), "unknown", "", gitWebhookCheck},
	} {
		forge, event, action := gitWebhookEvent(c.header)
		assert.Equal(t, forge, c.forge, "%v", c.header)
		assert.Equal(t, event, c.event, "%v", c.header)
		assert.Equal(t, action, c.action, "%v", c.header)
	}
}
