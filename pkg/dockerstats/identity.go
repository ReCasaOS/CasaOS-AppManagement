package dockerstats

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/docker/api/types/container"
)

const (
	groupApp   = "Conteneurs"
	groupOther = "Autres conteneurs"
)

// Identity is the stable identity of a container: it survives re-creation.
// Kind: "app-container" | "container".
type Identity struct{ ID, Name, Kind, Group, App string }

// token is what Docker accepts for a container name; compose project and
// service names are checked against it too, so ':' (the id separator) and
// control characters can never reach an id.
var token = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func appIdentity(project, service string, n int) Identity {
	name := service
	if n != 1 {
		name = service + "-" + strconv.Itoa(n)
	}
	return Identity{ID: "app:" + project + ":" + service + ":" + strconv.Itoa(n), Name: name, Kind: "app-container", Group: groupApp, App: project}
}

func otherIdentity(name string) Identity {
	return Identity{ID: "container:" + name, Name: name, Kind: "container", Group: groupOther}
}

// Identify names a container from its labels and Docker names. A compose
// container (project and service labels, not a one-off run) is identified by
// app, service and number; anything else by its name. false when nothing
// usable is available.
func Identify(labels map[string]string, names []string) (Identity, bool) {
	project, service := labels[api.ProjectLabel], labels[api.ServiceLabel]
	if token.MatchString(project) && token.MatchString(service) && !strings.EqualFold(labels[api.OneoffLabel], "true") {
		n, err := strconv.Atoi(labels[api.ContainerNumberLabel])
		if err != nil || n < 1 {
			n = 1
		}
		return appIdentity(project, service, n), true
	}
	for _, raw := range names {
		name := strings.TrimPrefix(raw, "/")
		if token.MatchString(name) { // a link ("/a/b") has a '/' and fails here
			return otherIdentity(name), true
		}
	}
	return Identity{}, false
}

// ParseID rebuilds an Identity from an id this service made (items known only
// from disk). false for any other id.
func ParseID(id string) (Identity, bool) {
	parts := strings.Split(id, ":")
	switch {
	case len(parts) == 2 && parts[0] == "container" && token.MatchString(parts[1]):
		return otherIdentity(parts[1]), true
	case len(parts) == 4 && parts[0] == "app" && token.MatchString(parts[1]) && token.MatchString(parts[2]):
		n, err := strconv.Atoi(parts[3])
		if err != nil || n < 1 || strconv.Itoa(n) != parts[3] {
			return Identity{}, false
		}
		return appIdentity(parts[1], parts[2], n), true
	}
	return Identity{}, false
}

// StateOf maps Docker's state to the contract's states. An exit with a
// non-zero code counts as a crash.
func StateOf(c container.Summary) string {
	switch string(c.State) {
	case "running":
		return "running"
	case "paused":
		return "paused"
	case "restarting", "dead":
		return "crashed"
	case "created":
		return "stopped"
	case "exited":
		if strings.HasPrefix(c.Status, "Exited (") && !strings.HasPrefix(c.Status, "Exited (0)") {
			return "crashed"
		}
		return "stopped"
	}
	return "unknown"
}

// HealthOf reads the health probe out of Docker's status text: 1 healthy,
// 0 unhealthy, ok=false when there is no probe or it is still starting.
func HealthOf(status string) (float64, bool) {
	switch {
	case strings.Contains(status, "(unhealthy)"):
		return 0, true
	case strings.Contains(status, "(healthy)"):
		return 1, true
	}
	return 0, false
}
