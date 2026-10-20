package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// statsSampler is the running sampler, nil when it is not; stats answers are live and never cached.
//
// The id of every stats route comes from the generated binder, which unescapes
// the parameter echo already unescaped, so `%253A` arrives as `:`. Harmless: the
// lookup is an exact key and an identity holds no `%`.
func statsSampler(ctx echo.Context) *dockerstats.Sampler {
	ctx.Response().Header().Set("Cache-Control", "no-store")
	return service.Stats()
}

// statsCode answers an error of the stats routes: the code alone, never the cause.
func statsCode(ctx echo.Context, status int, code string) error {
	return ctx.JSON(status, codegen.BaseResponse{Message: &code})
}

func statsFailure(ctx echo.Context, err error) error {
	var re *stats.RuleError
	switch {
	case errors.Is(err, dockerstats.ErrUnknownItem):
		return statsCode(ctx, http.StatusNotFound, "unknown_item")
	case errors.As(err, &re):
		return badRules(ctx, fmt.Sprintf("rules[%d].%s", re.Index, re.Field))
	case !errors.Is(err, dockerstats.ErrUnavailable):
		logger.Error("reading container stats failed", zap.Error(err))
	}
	return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
}

// StatsList lists the containers with their latest use. The body is bare, as the contract asks.
func (a *AppManagement) StatsList(ctx echo.Context) error {
	s := statsSampler(ctx)
	if s == nil {
		return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	items, err := s.Items()
	if err != nil {
		return statsFailure(ctx, err)
	}
	out := make([]codegen.StatsItem, 0, len(items))
	for _, it := range items {
		item := codegen.StatsItem{
			Id: it.ID, Name: it.Name, Kind: it.Kind, Group: it.Group, State: it.State,
			StartedAt: it.StartedAt, CpuPercent: it.CPUPercent, Alerts: it.Alerts,
		}
		if it.App != "" {
			item.App = utils.Ptr(it.App)
		}
		if it.Detail != "" {
			item.Detail = utils.Ptr(it.Detail)
		}
		if it.MemoryUsed != nil {
			item.MemoryUsed = utils.Ptr(int64(*it.MemoryUsed))
		}
		if it.MemoryLimit != nil {
			item.MemoryLimit = utils.Ptr(int64(*it.MemoryLimit))
		}
		out = append(out, item)
	}
	return ctx.JSON(http.StatusOK, codegen.StatsList{Items: out, Notes: s.Notes()})
}

type statsScale struct {
	CPU           string   `json:"cpu"`
	CPUAllocation *float64 `json:"cpu_allocation"`
	MemoryLimit   *uint64  `json:"memory_limit"`
}

// statsResult drops Result's MarshalJSON: embedded as is, it would be promoted
// and hide every field beside it.
type statsResult stats.Result

type statsSeries struct {
	ID string `json:"id"`
	statsResult
	Scale statsScale `json:"scale"`
	Notes []string   `json:"notes"`
}

// StatsSeries returns one container's history over range (live when absent).
func (a *AppManagement) StatsSeries(ctx echo.Context, id codegen.StatsItemID, _ codegen.StatsSeriesParams) error {
	s := statsSampler(ctx)
	if s == nil {
		return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	r := stats.RangeLive
	// read here, not from the bound params: `?range=` must be refused, not taken for
	// absent. A repeated range never gets here: the generated binder refuses it.
	if values, ok := ctx.QueryParams()["range"]; ok {
		var valid bool
		if r, valid = stats.ParseRange(values[0]); !valid {
			return statsCode(ctx, http.StatusBadRequest, "bad_range")
		}
	}
	v, err := s.Series(id, r)
	if err != nil {
		return statsFailure(ctx, err)
	}
	return ctx.JSON(http.StatusOK, statsSeries{
		ID:          id,
		statsResult: statsResult(v.Result),
		Scale:       statsScale{CPU: "percent_of_one_core", CPUAllocation: v.CPUAllocation, MemoryLimit: v.MemoryLimit},
		Notes:       v.Notes,
	})
}

type statsAlerts struct {
	Rules  []stats.Rule         `json:"rules"`
	Active []codegen.StatsAlert `json:"active"`
}

func statsAlertsOf(ctx echo.Context, s *dockerstats.Sampler, id string) error {
	rules, active, err := s.Alerts(id)
	if err != nil {
		return statsFailure(ctx, err)
	}
	out := statsAlerts{Rules: rules, Active: make([]codegen.StatsAlert, 0, len(active))}
	for _, e := range active {
		out.Active = append(out.Active, codegen.StatsAlert{Key: e.Key, Since: e.Since, Value: e.Value})
	}
	return ctx.JSON(http.StatusOK, out)
}

// StatsAlerts returns the rules of a container and its firing alerts.
func (a *AppManagement) StatsAlerts(ctx echo.Context, id codegen.StatsItemID) error {
	s := statsSampler(ctx)
	if s == nil {
		return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	return statsAlertsOf(ctx, s, id)
}

// maxRulesBody bounds a PUT of rules; two rules take about 150 bytes.
const maxRulesBody = 64 << 10

func badRules(ctx echo.Context, field string) error {
	return statsCode(ctx, http.StatusBadRequest, "bad_rules: "+field)
}

// SetStatsAlerts replaces the rules of a container. The request validator skips
// this route (route/v2.go): every malformed body, whatever its fault, is
// bad_rules naming the field, and the body is bounded before it is read.
func (a *AppManagement) SetStatsAlerts(ctx echo.Context, id codegen.StatsItemID) error {
	s := statsSampler(ctx)
	if s == nil {
		return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(ctx.Response(), ctx.Request().Body, maxRulesBody))
	var body struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Rules == nil {
		return badRules(ctx, "rules") // also too large, not JSON, not an object, no list
	}
	rules := make([]stats.Rule, len(body.Rules))
	for i, m := range body.Rules {
		// each field present (an absent one is empty, which Unmarshal refuses) and
		// of its type; the values are the Evaluator's to judge
		for _, f := range []struct {
			name string
			to   any
		}{{"key", &rules[i].Key}, {"enabled", &rules[i].Enabled}, {"threshold", &rules[i].Threshold}, {"minutes", &rules[i].Minutes}} {
			if v := m[f.name]; string(v) == "null" || json.Unmarshal(v, f.to) != nil {
				return badRules(ctx, fmt.Sprintf("rules[%d].%s", i, f.name))
			}
		}
	}
	if err := s.SetRules(id, rules); err != nil {
		return statsFailure(ctx, err)
	}
	return statsAlertsOf(ctx, s, id)
}
