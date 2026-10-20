package dockerstats

import (
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

func compose(project, service, number string) map[string]string {
	l := map[string]string{api.ProjectLabel: project, api.ServiceLabel: service}
	if number != "" {
		l[api.ContainerNumberLabel] = number
	}
	return l
}

func TestAComposeContainerIsIdentifiedByAppServiceAndNumber(t *testing.T) {
	id, ok := Identify(compose("jarvis", "web", "2"), []string{"/jarvis-web-2"})
	assert.Assert(t, ok)
	assert.Equal(t, id, Identity{ID: "app:jarvis:web:2", Name: "web-2", Kind: "app-container", Group: "Conteneurs", App: "jarvis"})

	for _, n := range []string{"1", ""} {
		id, ok = Identify(compose("jarvis", "web", n), []string{"/jarvis-web-1"})
		assert.Assert(t, ok)
		assert.Equal(t, id.ID, "app:jarvis:web:1")
		assert.Equal(t, id.Name, "web")
	}
}

func TestARecreatedContainerKeepsItsIdentity(t *testing.T) {
	a := container.Summary{ID: "aaa", Names: []string{"/jarvis-web-1"}, Labels: compose("jarvis", "web", "1")}
	b := container.Summary{ID: "bbb", Names: []string{"/jarvis-web-1"}, Labels: compose("jarvis", "web", "1")}
	ia, _ := Identify(a.Labels, a.Names)
	ib, _ := Identify(b.Labels, b.Names)
	assert.Equal(t, ia, ib)
}

func TestAnyOtherContainerIsIdentifiedByItsName(t *testing.T) {
	id, ok := Identify(nil, []string{"/my.db_1"})
	assert.Assert(t, ok)
	assert.Equal(t, id, Identity{ID: "container:my.db_1", Name: "my.db_1", Kind: "container", Group: "Autres conteneurs"})
}

func TestAComposeRunOneOffIsAnOtherContainer(t *testing.T) {
	l := compose("jarvis", "web", "1")
	l[api.OneoffLabel] = "True"
	id, ok := Identify(l, []string{"/jarvis-web-run-ab12"})
	assert.Assert(t, ok)
	assert.Equal(t, id.ID, "container:jarvis-web-run-ab12")
	assert.Equal(t, id.Kind, "container")
}

func TestNoNameMeansNoIdentity(t *testing.T) {
	_, ok := Identify(nil, nil)
	assert.Assert(t, !ok)
	id, ok := Identify(nil, []string{"/a/b", "/real"})
	assert.Assert(t, ok)
	assert.Equal(t, id.Name, "real")
	_, ok = Identify(nil, []string{"/a/b"})
	assert.Assert(t, !ok)
}

func TestHostileTextNeverBuildsAnIdentity(t *testing.T) {
	for _, n := range []string{"/bad:name", "/<script>", "/x\ny", "/" + strings.Repeat("a", 300), "/", "/-lead", "/trail\n"} {
		_, ok := Identify(nil, []string{n})
		assert.Assert(t, !ok, n)
	}
	// unusable compose labels fall back to the name, never into the id
	for _, bad := range [][2]string{{"a:b", "web"}, {"jarvis", "we b"}, {"", "web"}, {"jarvis", ""}, {"jar\x00vis", "web"}, {"jarvis", "web\n"}} {
		id, ok := Identify(compose(bad[0], bad[1], "1"), []string{"/jarvis-web-1"})
		assert.Assert(t, ok)
		assert.Equal(t, id.ID, "container:jarvis-web-1")
	}
	// a bad container number is read as 1
	id, _ := Identify(compose("jarvis", "web", "x:9"), []string{"/n"})
	assert.Equal(t, id.ID, "app:jarvis:web:1")
	id, _ = Identify(compose("jarvis", "web", "0"), []string{"/n"})
	assert.Equal(t, id.ID, "app:jarvis:web:1")
}

func TestParseIDRoundTrips(t *testing.T) {
	want, _ := Identify(compose("jarvis", "web", "2"), []string{"/jarvis-web-2"})
	got, ok := ParseID("app:jarvis:web:2")
	assert.Assert(t, ok)
	assert.Equal(t, got, want)

	got, ok = ParseID("container:x")
	assert.Assert(t, ok)
	assert.Equal(t, got, Identity{ID: "container:x", Name: "x", Kind: "container", Group: "Autres conteneurs"})

	for _, bad := range []string{"vm:abc", "host", "app:onlytwo", "app:a:b", "app:a:b:0", "app:a:b:02", "app:a:b:c", "app:a:b:1:2", "app:a:b:1\n", "container:", "container:a:b", "container:a/b", "app::b:1", "vm:a:b:1", "app:a:b c:1", "app:a:b\nx:1"} {
		_, ok = ParseID(bad)
		assert.Assert(t, !ok, bad)
	}
}

func TestStateOf(t *testing.T) {
	for _, c := range []struct{ state, status, want string }{
		{"running", "Up 2 hours", "running"},
		{"paused", "Up 2 hours (Paused)", "paused"},
		{"restarting", "Restarting (1) 5 seconds ago", "crashed"},
		{"dead", "Dead", "crashed"},
		{"exited", "Exited (0) 3 hours ago", "stopped"},
		{"exited", "Exited (1) 2 minutes ago", "crashed"},
		{"exited", "Exited (139) 2 minutes ago", "crashed"},
		{"exited", "Exited (137) 2 minutes ago", "stopped"},
		{"exited", "Exited (143) 2 minutes ago", "stopped"},
		{"exited", "", "stopped"},
		{"exited", "Exited (x) now", "stopped"},
		{"created", "Created", "stopped"},
		{"removing", "Removing", "unknown"},
		{"", "", "unknown"},
	} {
		assert.Equal(t, StateOf(container.Summary{State: container.ContainerState(c.state), Status: c.status}), c.want, c.state+"/"+c.status)
	}
}

func TestHealthOf(t *testing.T) {
	v, ok := HealthOf("Up 2 hours (healthy)")
	assert.Assert(t, ok && v == 1)
	v, ok = HealthOf("Up 2 hours (unhealthy)")
	assert.Assert(t, ok && v == 0)
	_, ok = HealthOf("Up 3 seconds (health: starting)")
	assert.Assert(t, !ok)
	_, ok = HealthOf("Up 2 hours")
	assert.Assert(t, !ok)
}

func TestTheOneOffLabelIsReadWhateverItsCase(t *testing.T) {
	for _, v := range []string{"True", "true", "TRUE"} {
		l := compose("jarvis", "web", "1")
		l[api.OneoffLabel] = v
		id, _ := Identify(l, []string{"/x"})
		assert.Equal(t, id.ID, "container:x", v)
	}
	l := compose("jarvis", "web", "1")
	l[api.OneoffLabel] = "False"
	id, _ := Identify(l, []string{"/x"})
	assert.Equal(t, id.ID, "app:jarvis:web:1")
}
