package service

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/inkly/CasaOS-AppManagement/common"
)

// A stack somebody wrote by hand has no `x-casaos` anywhere in it. Reading the main
// service out of StoreInfo therefore failed outright for those, and the Containers tab
// showed `extension `x-casaos` not found` instead of the containers -- on exactly the
// multi-service stacks it was added for.
func TestMainServiceNameDoesNotNeedTheCasaOSExtension(t *testing.T) {
	services := func(names ...string) types.Services {
		out := types.Services{}
		for _, name := range names {
			out[name] = types.ServiceConfig{Name: name}
		}
		return out
	}

	// no extension at all: alphabetically first, which is what StoreInfo falls back to
	// when the extension is there but names no main
	app := &ComposeApp{Name: "gluetun-stack", Services: services("qbittorrent", "gluetun")}
	if got := app.MainServiceName(); got != "gluetun" {
		t.Fatalf("a stack with no x-casaos must still have a main service, got %q", got)
	}

	// an extension that names one wins
	app.Extensions = types.Extensions{
		common.ComposeExtensionNameXCasaOS: map[string]interface{}{"main": "qbittorrent"},
	}
	if got := app.MainServiceName(); got != "qbittorrent" {
		t.Fatalf("x-casaos.main must win, got %q", got)
	}

	// an extension that names none falls back the same way
	app.Extensions = types.Extensions{
		common.ComposeExtensionNameXCasaOS: map[string]interface{}{"title": map[string]interface{}{"en_us": "Gluetun"}},
	}
	if got := app.MainServiceName(); got != "gluetun" {
		t.Fatalf("an x-casaos without main must fall back, got %q", got)
	}

	// and an app with no services at all is not a crash
	if got := (&ComposeApp{Name: "empty"}).MainServiceName(); got != "" {
		t.Fatalf("an app with no services has no main service, got %q", got)
	}
}
