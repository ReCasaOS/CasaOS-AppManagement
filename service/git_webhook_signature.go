package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// What a forge's webhook delivery says of itself: who signed it, which forge sent it, and
// which event it reports. Nothing here reads the body but to sign it.

// Forges, as the last delivery names them.
const (
	gitForgeGitHub  = "github"
	gitForgeGitea   = "gitea"
	gitForgeForgejo = "forgejo"
	gitForgeGogs    = "gogs"
	gitForgeGitLab  = "gitlab"
	gitForgeUnknown = "unknown"
)

// What a delivery asks for: gitWebhookCheck, or one of the answers GitWebhookPong and
// GitWebhookIgnored.
const gitWebhookCheck = "check"

// Answers a forge is given: 200 for GitWebhookPong, 202 for the others.
const (
	GitWebhookChecking  = "checking"
	GitWebhookCoalesced = "coalesced"
	GitWebhookQueued    = "queued"
	GitWebhookIgnored   = "ignored"
	GitWebhookPong      = "pong"
)

// gitWebhookEventCap is as much of an event's name as is logged and kept.
const gitWebhookEventCap = 64

// gitWebhookForges pairs each forge with its event and its signature header, the most
// specific first: Forgejo sends Gitea's and Gogs' headers too, and Gitea sends Gogs' and
// GitHub's.
var gitWebhookForges = []struct{ forge, event, signature string }{
	{gitForgeForgejo, "X-Forgejo-Event", "X-Forgejo-Signature"},
	{gitForgeGitea, "X-Gitea-Event", "X-Gitea-Signature"},
	{gitForgeGogs, "X-Gogs-Event", "X-Gogs-Signature"},
	{gitForgeGitLab, "X-Gitlab-Event", "X-Gitlab-Token"},
	{gitForgeGitHub, "X-GitHub-Event", "X-Hub-Signature-256"},
}

// gitWebhookSigned reports whether body was sent by someone holding secret: at least one
// signature header, and every signature header sent valid, each compared in constant time.
// A header that contradicts another is a request somebody changed on the way.
func gitWebhookSigned(header http.Header, body []byte, secret string) bool {
	if secret == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)

	signed := false
	for _, forge := range gitWebhookForges {
		for _, value := range header.Values(forge.signature) {
			if !gitWebhookSignatureValid(forge.signature, value, want, secret) {
				return false
			}
			signed = true
		}
	}

	return signed
}

// gitWebhookSignatureValid judges one signature header: GitLab's is the secret itself,
// GitHub's the hex HMAC-SHA256 of the body after `sha256=`, the others that HMAC alone.
func gitWebhookSignatureValid(name, value string, want []byte, secret string) bool {
	switch name {
	case "X-Gitlab-Token":
		return subtle.ConstantTimeCompare([]byte(value), []byte(secret)) == 1
	case "X-Hub-Signature-256":
		hexed, ok := strings.CutPrefix(value, "sha256=")
		if !ok {
			return false
		}
		value = hexed
	}

	got, err := hex.DecodeString(value)

	return err == nil && hmac.Equal(got, want)
}

// gitWebhookEvent names the forge after its event header, else after its signature header,
// else `unknown`; and says what the delivery asks for: a push is checked, GitHub's ping
// is answered pong, any other named event is ignored, and a delivery naming no event, from
// a less common forge, is checked.
func gitWebhookEvent(header http.Header) (forge, event, action string) {
	for _, f := range gitWebhookForges {
		if values := header.Values(f.event); len(values) > 0 {
			forge, event = f.forge, values[0]
			break
		}
	}

	if forge == "" {
		forge = gitForgeUnknown
		for _, f := range gitWebhookForges {
			if len(header.Values(f.signature)) > 0 {
				forge = f.forge
				break
			}
		}

		return forge, "", gitWebhookCheck
	}

	if len(event) > gitWebhookEventCap {
		event = event[:gitWebhookEventCap]
	}

	switch {
	case forge == gitForgeGitHub && event == "ping":
		return forge, event, GitWebhookPong
	case forge == gitForgeGitLab && (event == "Push Hook" || event == "Tag Push Hook"),
		forge != gitForgeGitLab && event == "push":
		return forge, event, gitWebhookCheck
	}

	return forge, event, GitWebhookIgnored
}
