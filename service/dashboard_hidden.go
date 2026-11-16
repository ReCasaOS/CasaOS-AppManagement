package service

import (
	stdjson "encoding/json"
	"os"
	"slices"
	"sync"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// DashboardHiddenPath lists the apps whose icon the dashboard leaves out, for every
// account. It is a display choice and no protection: the file is apart from
// visibility.json so that nothing here can change who may see an app.
var DashboardHiddenPath = "/var/lib/casaos/dashboard-hidden.json"

// DashboardHidden is the box's one list.
var DashboardHidden = &dashboardHidden{}

type dashboardHidden struct {
	mu sync.RWMutex
}

func (d *dashboardHidden) read() ([]string, error) {
	content, err := os.ReadFile(DashboardHiddenPath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	apps := []string{}
	if err := stdjson.Unmarshal(content, &apps); err != nil {
		return nil, err
	}

	return apps, nil
}

// List returns the hidden apps. A file that cannot be read hides nothing: an icon shown
// again is the safe side of a display choice.
func (d *dashboardHidden) List() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	apps, err := d.read()
	if err != nil {
		logger.Error("the list of apps hidden from the dashboard cannot be read, so none is hidden", zap.Error(err), zap.String("path", DashboardHiddenPath))
		return []string{}
	}

	return apps
}

// Set hides app from the dashboard or shows it again.
func (d *dashboardHidden) Set(app string, hidden bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	apps, err := d.read()
	if err != nil {
		return err
	}
	if slices.Contains(apps, app) == hidden {
		return nil
	}
	if hidden {
		apps = append(apps, app)
	} else {
		apps = slices.DeleteFunc(apps, func(other string) bool { return other == app })
	}
	slices.Sort(apps)

	encoded, err := stdjson.MarshalIndent(apps, "", "  ")
	if err != nil {
		return err
	}

	return writeGitFile(DashboardHiddenPath, encoded) // temporary file, sync, rename, 0600
}
