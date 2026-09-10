package v2

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/docker/compose/v2/pkg/api"
)

func publishers(mappings ...[3]interface{}) api.PortPublishers {
	out := api.PortPublishers{}
	for _, m := range mappings {
		out = append(out, api.PortPublisher{
			PublishedPort: m[0].(int),
			TargetPort:    m[1].(int),
			Protocol:      m[2].(string),
		})
	}
	return out
}

func container(p api.PortPublishers) codegen.ContainerSummary {
	return codegen.ContainerSummary{Publishers: p}
}

// The stack that started this: gluetun holds the network and publishes everything,
// qbittorrent routes through it and publishes nothing of its own. Three ports come
// out of one service, and only one of them is a web interface -- the lowest is the
// BitTorrent port, which is why "take the first" is not an answer here.
func TestTheWebUIPortOfAVPNRoutedStack(t *testing.T) {
	got := inferWebUIPort(map[string][]codegen.ContainerSummary{
		"gluetun": {container(publishers(
			[3]interface{}{6881, 6881, "tcp"},
			[3]interface{}{6881, 6881, "udp"},
			[3]interface{}{8080, 8080, "tcp"},
			[3]interface{}{8102, 8102, "tcp"},
			[3]interface{}{8102, 8102, "udp"},
		))},
		"qbittorrent": {container(nil)},
	}, "gluetun")

	if got != "8080" {
		t.Fatalf("the qBittorrent web UI is on 8080, got %q", got)
	}
}

func TestInferWebUIPort(t *testing.T) {
	cases := []struct {
		name       string
		containers map[string][]codegen.ContainerSummary
		main       string
		want       string
	}{
		{
			"one published port is not a guess",
			map[string][]codegen.ContainerSummary{"app": {container(publishers([3]interface{}{9925, 8384, "tcp"}))}},
			"app", "9925",
		},
		{
			// nothing here says which of two unrelated ports is a web interface
			"two ports and neither is a web port",
			map[string][]codegen.ContainerSummary{"app": {container(publishers(
				[3]interface{}{6881, 6881, "tcp"},
				[3]interface{}{51413, 51413, "tcp"},
			))}},
			"app", "",
		},
		{
			"the container's own port decides, not the host's",
			map[string][]codegen.ContainerSummary{"app": {container(publishers(
				[3]interface{}{32771, 9091, "tcp"},
				[3]interface{}{32772, 80, "tcp"},
			))}},
			"app", "32772",
		},
		{
			"a published port that is itself well known counts too",
			map[string][]codegen.ContainerSummary{"app": {container(publishers(
				[3]interface{}{7777, 7777, "tcp"},
				[3]interface{}{8080, 34567, "tcp"},
			))}},
			"app", "8080",
		},
		{
			"udp alone is not a web interface",
			map[string][]codegen.ContainerSummary{"app": {container(publishers([3]interface{}{51820, 51820, "udp"}))}},
			"app", "",
		},
		{
			"a container port that was never published is not offered",
			map[string][]codegen.ContainerSummary{"app": {container(publishers([3]interface{}{0, 8080, "tcp"}))}},
			"app", "",
		},
		{
			// Docker lists one publisher per address family on a dual-stack host
			"the same mapping twice is one port, not two",
			map[string][]codegen.ContainerSummary{"app": {
				container(publishers([3]interface{}{8384, 8384, "tcp"})),
				container(publishers([3]interface{}{8384, 8384, "tcp"})),
			}},
			"app", "8384",
		},
		{
			"a main service that publishes nothing lets the others answer",
			map[string][]codegen.ContainerSummary{
				"db":  {container(nil)},
				"web": {container(publishers([3]interface{}{8096, 8096, "tcp"}))},
			},
			"db", "8096",
		},
		{
			// name order, so two refreshes cannot disagree
			"two services that both publish answer in name order",
			map[string][]codegen.ContainerSummary{
				"alpha": {container(publishers([3]interface{}{3000, 3000, "tcp"}))},
				"beta":  {container(publishers([3]interface{}{5000, 5000, "tcp"}))},
				"main":  {container(nil)},
			},
			"main", "3000",
		},
		{"no containers at all", map[string][]codegen.ContainerSummary{}, "app", ""},
		{"a service with no containers", map[string][]codegen.ContainerSummary{"app": nil}, "app", ""},
		{"a main service that is not in the map", map[string][]codegen.ContainerSummary{"other": {container(nil)}}, "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferWebUIPort(c.containers, c.main); got != c.want {
				t.Fatalf("want %q, got %q", c.want, got)
			}
		})
	}
}

// The same input must give the same answer, or the dashboard offers a different
// port every time somebody reloads it.
func TestInferWebUIPortIsStableAcrossMapOrder(t *testing.T) {
	containers := map[string][]codegen.ContainerSummary{
		"main":  {container(nil)},
		"alpha": {container(publishers([3]interface{}{3000, 3000, "tcp"}))},
		"beta":  {container(publishers([3]interface{}{5000, 5000, "tcp"}))},
		"gamma": {container(publishers([3]interface{}{8000, 8000, "tcp"}))},
	}

	for i := 0; i < 50; i++ {
		if got := inferWebUIPort(containers, "main"); got != "3000" {
			t.Fatalf("run %d answered %q", i, got)
		}
	}
}
