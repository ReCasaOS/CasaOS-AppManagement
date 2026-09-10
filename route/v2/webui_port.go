package v2

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// Ports an image serves a web interface on, best first.
//
// This list only ever decides BETWEEN ports a container already publishes, and
// only when the compose file names none itself. A published port matching none
// of these is not offered at all: choosing between a torrent port and an admin
// panel by picking the lower number is worse than saying there is nothing to
// open, because the wrong answer looks like a working one until it is clicked.
//
// ponytail: a fixed list ages with the ecosystem. Settings > Web UI writes
// `x-casaos.port_map` and wins over everything here, so being wrong costs one
// field rather than a release.
var webUIPorts = []int{80, 8080, 443, 8000, 3000, 5000, 8081, 8443, 9000, 8096}

// inferWebUIPort answers which published host port a compose app can be opened
// on, for an app whose compose file does not say.
//
// The main service is asked first, and that is not a preference but the shape of
// these stacks: a service routed through another (`network_mode: service:vpn`)
// publishes nothing of its own, and every port of the stack belongs to the one
// holding the network. Only when the main service publishes nothing do the
// others get a turn, in name order — a grid that offers a different port on
// every refresh is worse than one that offers none.
//
// An empty answer means exactly that: nothing here is worth opening.
func inferWebUIPort(containers map[string][]codegen.ContainerSummary, main string) string {
	if port := webUIPortOf(containers[main]); port != "" {
		return port
	}

	others := make([]string, 0, len(containers))
	for service := range containers {
		if service != main {
			others = append(others, service)
		}
	}
	sort.Strings(others)

	for _, service := range others {
		if port := webUIPortOf(containers[service]); port != "" {
			return port
		}
	}

	return ""
}

// webUIPortOf answers for the containers of a single service.
func webUIPortOf(list []codegen.ContainerSummary) string {
	target := map[int]int{}
	published := []int{}

	for _, container := range list {
		for _, publisher := range container.Publishers {
			// A container port that was never published arrives with PublishedPort 0,
			// and a UDP mapping is not a web interface. Docker also lists one publisher
			// per address family, so a dual-stack host reports each mapping twice.
			if publisher.PublishedPort == 0 {
				continue
			}
			if protocol := strings.ToLower(publisher.Protocol); protocol != "" && protocol != "tcp" {
				continue
			}
			if _, seen := target[publisher.PublishedPort]; seen {
				continue
			}

			target[publisher.PublishedPort] = publisher.TargetPort
			published = append(published, publisher.PublishedPort)
		}
	}

	if len(published) == 0 {
		return ""
	}

	// One published port is not a guess. It is the only thing there is to open, and
	// an app that publishes exactly one port is most of them.
	if len(published) == 1 {
		return strconv.Itoa(published[0])
	}

	sort.Ints(published)

	// Several. The container's own port decides, because that is the one the image
	// chose; the host side is whatever happened to be free when somebody wrote the
	// compose file. The host side is still consulted, since a compose file that
	// publishes 8080 usually means it.
	for _, wanted := range webUIPorts {
		for _, port := range published {
			if target[port] == wanted || port == wanted {
				return strconv.Itoa(port)
			}
		}
	}

	return ""
}
