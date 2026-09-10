package v2

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/inkly/CasaOS-AppManagement/common"
	"github.com/inkly/CasaOS-AppManagement/service"
)

// A compose file written by hand carries no `x-casaos` at all. Reading
// is_uncontrolled out of it used to be one expression whose first type assertion had no
// comma-ok, so a nil interface was asserted to a map type -- which panics.
//
// The app grid asks this about EVERY installed app, so one such stack on the host took
// casaos-app-management down on every request: the dashboard came back 502, systemd
// restarted the service, the grid killed it again. It was unreachable only while the
// caller gave up on those apps before getting here, which is precisely what was removed
// to stop the grid drawing them as stopped.
func TestIsUncontrolledSurvivesAComposeFileWithNoExtension(t *testing.T) {
	// no extensions map at all
	if got := isUncontrolled(&service.ComposeApp{Name: "gluetun-stack"}); got {
		t.Fatalf("an app with no x-casaos is not uncontrolled, got %v", got)
	}

	// an extensions map that does not carry x-casaos
	if got := isUncontrolled(&service.ComposeApp{
		Name:       "deploy",
		Extensions: types.Extensions{"x-something-else": map[string]interface{}{"a": 1}},
	}); got {
		t.Fatalf("an unrelated extension is not x-casaos, got %v", got)
	}

	// x-casaos present but not a map -- the shape the assertion assumed
	if got := isUncontrolled(&service.ComposeApp{
		Name:       "odd",
		Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: "not a map"},
	}); got {
		t.Fatalf("a malformed extension is not a reason to crash, got %v", got)
	}

	// x-casaos present, key absent
	if got := isUncontrolled(&service.ComposeApp{
		Name:       "store-app",
		Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{"main": "app"}},
	}); got {
		t.Fatalf("an app that does not say it is uncontrolled is not, got %v", got)
	}

	// and the value is still read when it is there
	for _, want := range []bool{true, false} {
		got := isUncontrolled(&service.ComposeApp{
			Name: "adopted",
			Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{
				common.ComposeExtensionPropertyNameIsUncontrolled: want,
			}},
		})
		if got != want {
			t.Fatalf("is_uncontrolled=%v must be read as %v, got %v", want, want, got)
		}
	}
}
