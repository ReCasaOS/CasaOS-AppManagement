package service

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/inkly/CasaOS-AppManagement/common"
)

// Reading is_uncontrolled used to be one expression whose FIRST type assertion had no
// comma-ok, so a nil interface was asserted to a map type -- which panics.
//
// Two ways in. A compose file written by hand carries no `x-casaos` at all: the copy of
// this line in the app grid hit that, and since the grid asks about EVERY installed app,
// one such stack on the host took casaos-app-management down on every request -- 502,
// systemd restart, killed again. And `x-casaos:` with nothing after it puts a nil value
// under a present key, which walks straight past a guard that only tests whether the key
// exists.
func TestIsUncontrolledSurvivesEveryShapeOfExtension(t *testing.T) {
	cases := []struct {
		name string
		app  *ComposeApp
		want bool
	}{
		{"no extensions at all", &ComposeApp{Name: "gluetun-stack"}, false},
		{
			"extensions without x-casaos",
			&ComposeApp{Name: "deploy", Extensions: types.Extensions{"x-something-else": map[string]interface{}{"a": 1}}},
			false,
		},
		{
			// `x-casaos:` and nothing after it: the key is there, the value is nil
			"x-casaos present but nil",
			&ComposeApp{Name: "empty", Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: nil}},
			false,
		},
		{
			"x-casaos that is not a map",
			&ComposeApp{Name: "odd", Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: "not a map"}},
			false,
		},
		{
			"x-casaos without the key",
			&ComposeApp{Name: "store-app", Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{"main": "app"}}},
			false,
		},
		{
			"the key set to false",
			&ComposeApp{Name: "controlled", Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{
				common.ComposeExtensionPropertyNameIsUncontrolled: false,
			}}},
			false,
		},
		{
			"the key set to true",
			&ComposeApp{Name: "adopted", Extensions: types.Extensions{common.ComposeExtensionNameXCasaOS: map[string]interface{}{
				common.ComposeExtensionPropertyNameIsUncontrolled: true,
			}}},
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.app.IsUncontrolled(); got != c.want {
				t.Fatalf("want %v, got %v", c.want, got)
			}
		})
	}
}
