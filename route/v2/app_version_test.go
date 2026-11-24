package v2

import (
	"testing"

	"github.com/docker/compose/v5/pkg/api"
)

func TestAppVersionOf(t *testing.T) {
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	with := func(labels map[string]string) []api.ContainerSummary {
		return []api.ContainerSummary{{Labels: labels}}
	}

	cases := []struct {
		name       string
		containers []api.ContainerSummary
		image      string
		version    string
		notes      string
	}{
		{"label wins over tag", with(map[string]string{labelImageVersion: "10.9.1-ls12", labelImageSource: "https://github.com/linuxserver/docker-jellyfin"}), "lscr.io/linuxserver/jellyfin:latest", "10.9.1-ls12", "https://github.com/linuxserver/docker-jellyfin/releases"},
		{"tag when no label", with(nil), "postgres:16.4", "16.4", "<nil>"},
		{"latest says nothing", with(nil), "nginx:latest", "<nil>", "<nil>"},
		{"no tag says nothing", with(nil), "nginx", "<nil>", "<nil>"},
		{"tag with digest", nil, "redis:7.2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "7.2", "<nil>"},
		{"registry with port", nil, "registry.local:5000/app:2.1", "2.1", "<nil>"},
		{"source .git and extra path", with(map[string]string{labelImageSource: "https://github.com/syncthing/syncthing.git/tree/main"}), "x", "<nil>", "https://github.com/syncthing/syncthing/releases"},
		{"not https", with(map[string]string{labelImageSource: "http://github.com/a/b"}), "x", "<nil>", "<nil>"},
		{"not github", with(map[string]string{labelImageSource: "https://gitlab.com/a/b"}), "x", "<nil>", "<nil>"},
		{"javascript", with(map[string]string{labelImageSource: "javascript:alert(1)//github.com/a/b"}), "x", "<nil>", "<nil>"},
		{"owner only", with(map[string]string{labelImageSource: "https://github.com/a"}), "x", "<nil>", "<nil>"},
		{"lookalike host", with(map[string]string{labelImageSource: "https://github.com.evil.example/a/b"}), "x", "<nil>", "<nil>"},
	}
	for _, c := range cases {
		v, n := appVersionOf(c.containers, c.image)
		if str(v) != c.version || str(n) != c.notes {
			t.Errorf("%s: got (%s, %s), want (%s, %s)", c.name, str(v), str(n), c.version, c.notes)
		}
	}
}
