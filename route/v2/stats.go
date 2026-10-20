package v2

import (
	"errors"
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

// statsCode answers an error of the stats routes: the code alone, never the cause.
func statsCode(ctx echo.Context, status int, code string) error {
	return ctx.JSON(status, codegen.BaseResponse{Message: &code})
}

func statsFailure(ctx echo.Context, err error) error {
	switch {
	case errors.Is(err, dockerstats.ErrUnknownItem):
		return statsCode(ctx, http.StatusNotFound, "unknown_item")
	case !errors.Is(err, dockerstats.ErrUnavailable):
		logger.Error("reading container stats failed", zap.Error(err))
	}
	return statsCode(ctx, http.StatusServiceUnavailable, "stats_unavailable")
}

// StatsList lists the containers with their latest use. The body is bare, as the contract asks.
func (a *AppManagement) StatsList(ctx echo.Context) error {
	s := service.Stats()
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
	s := service.Stats()
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

func (a *AppManagement) StatsAlerts(ctx echo.Context, _ codegen.StatsItemID) error {
	return statsCode(ctx, http.StatusNotImplemented, "not_implemented")
}

func (a *AppManagement) SetStatsAlerts(ctx echo.Context, _ codegen.StatsItemID) error {
	return statsCode(ctx, http.StatusNotImplemented, "not_implemented")
}
