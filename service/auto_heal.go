package service

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// AutoHealPath is where the switch for the automatic restart of an unhealthy app is kept.
// A variable so a test can put it somewhere of its own.
var AutoHealPath = "/var/lib/casaos/auto-heal.json"

// AutoHealEnabled is on unless the owner turned it off: a file that is absent, unreadable or
// malformed reads as on, and a read never rewrites it.
func AutoHealEnabled() bool {
	content, err := os.ReadFile(AutoHealPath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Info("auto heal: its switch cannot be read, taken as on", zap.Error(err))
		}

		return true
	}
	var state struct {
		Enabled *bool `json:"enabled"`
	}
	if err := stdjson.Unmarshal(content, &state); err != nil || state.Enabled == nil {
		logger.Info("auto heal: its switch is malformed, taken as on", zap.Error(err))

		return true
	}

	return *state.Enabled
}

// SetAutoHeal writes the switch beside and renames it: a file cut short would turn it on again.
func SetAutoHeal(enabled bool) error {
	encoded, err := stdjson.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	dir := filepath.Dir(AutoHealPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".auto-heal-*.tmp")
	if err != nil {
		return err
	}
	_, err = temp.Write(encoded)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(temp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(temp.Name(), AutoHealPath)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
	}

	return err
}
