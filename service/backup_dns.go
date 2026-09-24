package service

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// servesDNS says whether an app answers DNS: it publishes port 53, or it is AdGuard
// Home or Pi-hole on the host's network, where no port is published to look at.
//
// Such an app is never held still for a backup. Stopped for the copy, it takes the
// names away from every device that asks it, the box included when the box uses
// it: a copy to a cloud destination then fails at its first lookup
// (ReCasaOS/CasaOS#7), and the whole network loses its names for as long as the
// copy lasts. Its files are copied while it runs, and the run says so.
func servesDNS(services types.Services) bool {
	for _, service := range services {
		for _, port := range service.Ports {
			if port.Published == "53" {
				return true
			}
		}

		image := strings.ToLower(service.Image)
		if service.NetworkMode == "host" && (strings.Contains(image, "adguardhome") || strings.Contains(image, "pihole")) {
			return true
		}
	}

	return false
}
