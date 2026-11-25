package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"go.uber.org/zap"
)

// Before an update recreates the containers, the optional scan module is asked whether the
// new images carry more critical vulnerabilities than the ones running. Only a clear
// "worse" stops the update: no module, an old module, an error, a scan that is not done in
// time all let it through. A broken scan must never freeze app updates.

// BlockedImage is one service whose new image is worse than the one it runs.
type BlockedImage struct {
	Service     string                   `json:"service"` // compose service
	Image       string                   `json:"image"`   // reference in the new compose
	RepoDigests []string                 `json:"repo_digests"`
	ID          string                   `json:"id"` // ImageID of the candidate
	Current     int                      `json:"current"`
	Candidate   int                      `json:"candidate"`
	Added       []codegen.BlockedFinding `json:"added"`
}

// UpdateBlockedError is what checkCandidates returns when an update is refused.
type UpdateBlockedError struct{ Images []BlockedImage }

func (e *UpdateBlockedError) Error() string {
	more := 0
	for _, i := range e.Images {
		more += i.Candidate - i.Current
	}

	return fmt.Sprintf("the new version has %d more critical vulnerabilities than the one installed", more)
}

// gateTimeout covers the whole app, all its services together.
var gateTimeout = 10 * time.Minute

const (
	vulnScanURLFile = "vuln-scan.url"
	updateUnchecked = "update-unchecked"
	verdictWorse    = "worse"
	verdictNotWorse = "not_worse"
	verdictUnknown  = "unknown"
)

// compareImages asks the module. A variable so a test can replace it. Any answer but a 200
// is an error (an old module says 404); an "unknown" verdict comes with its reason as the error.
var compareImages = func(ctx context.Context, current, candidate string) (string, int, int, []codegen.BlockedFinding, error) {
	base, err := os.ReadFile(filepath.Join(config.CommonInfo.RuntimePath, vulnScanURLFile))
	if err != nil {
		return "", 0, 0, nil, err
	}
	body, _ := json.Marshal(map[string]string{"current": current, "candidate": candidate})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(string(base))+"/v2/vuln_scan/compare", bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, nil, err
	}
	// the internal secret goes to the module on this box, nowhere else
	if h := req.URL.Hostname(); req.URL.Scheme != "http" || (h != "127.0.0.1" && h != "::1" && h != "localhost") {
		return "", 0, 0, nil, fmt.Errorf("the scan module address is not on loopback: %s", req.URL.Redacted())
	}
	req.Header.Set("Content-Type", "application/json")
	if err := external.InternalRequestEditor(config.CommonInfo.RuntimePath)(ctx, req); err != nil {
		return "", 0, 0, nil, err
	}

	// no timeout of its own: the scans take seconds to minutes, ctx carries the limit.
	// A redirect is not followed: it would carry the secret elsewhere.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noRedirect.Do(req)
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", 0, 0, nil, fmt.Errorf("the scan module answered %d", res.StatusCode)
	}

	var out struct {
		Data struct {
			Verdict           string                   `json:"verdict"`
			CurrentCritical   int                      `json:"current_critical"`
			CandidateCritical int                      `json:"candidate_critical"`
			Added             []codegen.BlockedFinding `json:"added"`
			Reason            string                   `json:"reason"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return "", 0, 0, nil, err
	}
	d := out.Data
	if d.Verdict == verdictUnknown {
		reason := d.Reason
		if reason == "" {
			reason = "the scan module could not tell"
		}
		return d.Verdict, d.CurrentCritical, d.CandidateCritical, d.Added, errors.New(reason)
	}

	return d.Verdict, d.CurrentCritical, d.CandidateCritical, d.Added, nil
}

// scanModulePresent says the scan module is installed: it writes its address on this box.
func scanModulePresent() bool {
	_, err := os.Stat(filepath.Join(config.CommonInfo.RuntimePath, vulnScanURLFile))
	return err == nil
}

// checkCandidates compares, service by service, the image the app runs with the one the
// update pulled. It returns nil or *UpdateBlockedError, nothing else. A variable so a test
// can replace it.
var checkCandidates = func(ctx context.Context, cli imageTagger, running *ComposeApp, next *ComposeApp) error {
	if !scanModulePresent() {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()

	current := map[string]string{}
	if listed, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+running.Name),
	}); err == nil {
		for _, c := range listed.Items {
			if service := c.Labels[api.ServiceLabel]; service != "" && c.ImageID != "" {
				current[service] = c.ImageID
			}
		}
	}

	names := make([]string, 0, len(next.Services))
	for name := range next.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var blocked []BlockedImage
	published, noted := false, false
	unchecked := func(reason string) {
		if !noted {
			noted = true
			RecordAppEvent(running.Name, updateUnchecked, "", "", reason)
		}
	}
	for _, name := range names {
		ref := next.Services[name].Image
		old := current[name]
		if ref == "" || old == "" {
			continue
		}
		inspected, err := cli.ImageInspect(ctx, ref)
		if err != nil || inspected.ID == "" || inspected.ID == old {
			continue
		}

		if !published {
			published = true
			PublishInstallStep(ctx, InstallStepCheck)
		}
		verdict, cur, cand, added, err := compareImages(ctx, old, inspected.ID)
		if ctx.Err() != nil {
			unchecked("the scan did not finish in time")
			break
		}
		if verdict == verdictWorse && err == nil {
			blocked = append(blocked, BlockedImage{
				Service: name, Image: ref, RepoDigests: inspected.RepoDigests, ID: inspected.ID,
				Current: cur, Candidate: cand, Added: added,
			})
			continue
		}
		if verdict != verdictNotWorse || err != nil {
			reason := "the scan module could not tell"
			if err != nil {
				reason = err.Error()
			}
			logger.Info("an image could not be compared before the update", zap.String("app", running.Name), zap.String("service", name), zap.String("reason", reason))
			unchecked(reason)
		}
	}

	if len(blocked) > 0 {
		return &UpdateBlockedError{Images: blocked}
	}

	return nil
}
