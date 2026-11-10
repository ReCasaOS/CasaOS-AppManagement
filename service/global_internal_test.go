package service

import (
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

// A global variable is kept as one line, key=value. A key or a value the file
// cannot give back as it was is refused when it is set, not lost at the next start.
func TestChangeGlobalRefusesWhatTheFileCannotHold(t *testing.T) {
	logger.LogInitConsoleOnly()
	path, global := config.AppManagementGlobalEnvFilePath, config.Global
	config.AppManagementGlobalEnvFilePath, config.Global = filepath.Join(t.TempDir(), "env"), map[string]string{}
	defer func() { config.AppManagementGlobalEnvFilePath, config.Global = path, global }()

	twoLines := `first
SECOND=second`
	for what, entry := range map[string][2]string{
		"an equal sign in the key": {"A=B", "c"},
		"two lines in the key":     {twoLines, "c"},
		"two lines in the value":   {"KEY", twoLines},
		"a carriage return":        {"KEY", "value" + string(rune(13))},
	} {
		if err := (&AppStoreManagement{}).ChangeGlobal(entry[0], entry[1]); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	if len(config.Global) != 0 {
		t.Errorf("kept %v", config.Global)
	}
}
