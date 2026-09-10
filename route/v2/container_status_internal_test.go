package v2

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// The app named in the path is the only thing authorising the container named in
// the path. Without this check the route is "stop any container on the host",
// reachable through any app the caller can name -- so it is worth its own test
// rather than being trusted to the handler reading correctly.
func TestOnlyAnAppsOwnContainersCanBeActedOn(t *testing.T) {
	app := map[string][]codegen.ContainerSummary{
		"gluetun":     {{ID: "3f1c9a2b4d5e"}},
		"qbittorrent": {{ID: "aa11bb22cc33"}, {ID: "dd44ee55ff66"}},
		"init":        nil,
	}

	for _, id := range []string{"3f1c9a2b4d5e", "aa11bb22cc33", "dd44ee55ff66"} {
		if !containerBelongsTo(app, id) {
			t.Errorf("%s is one of this app's containers", id)
		}
	}

	for _, id := range []string{
		"",                          // no id at all
		"3f1c",                      // a prefix Docker would accept, which this route must not
		"3F1C9A2B4D5E",              // same id, other case
		"9999999999999",             // somebody else's container
		"aa11bb22cc33 dd44ee55ff66", // two ids in a trench coat
	} {
		if containerBelongsTo(app, id) {
			t.Errorf("%q is not one of this app's containers", id)
		}
	}

	if containerBelongsTo(map[string][]codegen.ContainerSummary{}, "3f1c9a2b4d5e") {
		t.Error("an app with no containers owns none")
	}
}
