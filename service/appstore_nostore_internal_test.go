package service

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
)

// A stack somebody wrote by hand carries no `x-casaos`, and one adopted from elsewhere
// may carry one that names no store app. Both used to end the update decision right
// there, before it reached the registry answer -- so the card wore the update badge,
// which comes from that answer, and the button replied `is up to date` in the same
// breath. Neither has a catalogue entry, which is an answer: the registry check is the
// whole of it, and an update re-pulls the tags the app already names.
func TestAnAppWithNoCatalogueEntryIsDecidedByTheRegistryCheck(t *testing.T) {
	logger.LogInitConsoleOnly()

	imageUpdates.Lock()
	imageUpdates.registry = map[string]bool{"gluetun-stack": true, "quiet-stack": false}
	imageUpdates.Unlock()

	t.Cleanup(func() {
		imageUpdates.Lock()
		imageUpdates.registry = map[string]bool{}
		imageUpdates.Unlock()
	})

	management := &AppStoreManagement{}

	// no x-casaos at all
	moved := &ComposeApp{Name: "gluetun-stack", Services: types.Services{"gluetun": types.ServiceConfig{Name: "gluetun"}}}
	available, reason, err := management.isUpdateAvailable(moved)
	if err != nil {
		t.Fatalf("a stack with no x-casaos is not an error: %v", err)
	}
	if !available {
		t.Fatal("the registries said this app's image moved, so the button must act on it")
	}
	if reason != "" {
		t.Fatalf("an update on offer has no reason to give, got %q", reason)
	}

	// same shape, nothing moved
	still := &ComposeApp{Name: "quiet-stack", Services: types.Services{"a": types.ServiceConfig{Name: "a"}}}
	if available, _, err := management.isUpdateAvailable(still); err != nil || available {
		t.Fatalf("nothing moved, so nothing is on offer (available=%v err=%v)", available, err)
	}

	// an x-casaos that names no store app is the same case
	adopted := &ComposeApp{
		Name:       "gluetun-stack",
		Services:   types.Services{"gluetun": types.ServiceConfig{Name: "gluetun"}},
		Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{"main": "gluetun"}},
	}
	if available, _, err := management.isUpdateAvailable(adopted); err != nil || !available {
		t.Fatalf("an extension that names no store app has no catalogue entry either (available=%v err=%v)", available, err)
	}
}
