package service

import (
	"context"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/compose-spec/compose-go/v2/types"
)

func TestAnAppThatAnswersDNSIsRecognised(t *testing.T) {
	for name, tc := range map[string]struct {
		service types.ServiceConfig
		dns     bool
	}{
		"AdGuard Home publishing 53": {types.ServiceConfig{Image: "adguard/adguardhome:latest", Ports: []types.ServicePortConfig{
			{Target: 53, Published: "53", Protocol: "udp"}, {Target: 3000, Published: "3000", Protocol: "tcp"},
		}}, true},
		"Pi-hole publishing 53 over tcp": {types.ServiceConfig{Image: "pihole/pihole:2025.08.0", Ports: []types.ServicePortConfig{
			{Target: 53, Published: "53", Protocol: "tcp"},
		}}, true},
		"AdGuard Home on the host's network": {types.ServiceConfig{Image: "adguard/adguardhome", NetworkMode: "host"}, true},
		"Pi-hole on the host's network":      {types.ServiceConfig{Image: "docker.io/PiHole/PiHole:latest", NetworkMode: "host"}, true},
		"any image publishing 53":            {types.ServiceConfig{Image: "coredns/coredns", Ports: []types.ServicePortConfig{{Target: 5353, Published: "53"}}}, true},
		"53 inside only":                     {types.ServiceConfig{Image: "adguard/adguardhome", Ports: []types.ServicePortConfig{{Target: 53, Published: "5353"}}}, false},
		"a web app on the host's network":    {types.ServiceConfig{Image: "jellyfin/jellyfin", NetworkMode: "host"}, false},
		"a web app":                          {types.ServiceConfig{Image: "nginx", Ports: []types.ServicePortConfig{{Target: 80, Published: "8080"}}}, false},
	} {
		if got := servesDNS(types.Services{"app": tc.service}); got != tc.dns {
			t.Errorf("%s: servesDNS = %v, want %v", name, got, tc.dns)
		}
	}
}

// Held still, a DNS server would take the box's own lookups down with it, and the
// copy to a cloud destination would fail at its first one: it is copied running,
// and the run says it was not stopped.
func TestAnAppThatAnswersDNSIsNotStoppedForItsBackup(t *testing.T) {
	app, _ := appWithOneBindAndOneVolume(t)
	service := app.Services["app"]
	service.Image = "adguard/adguardhome:latest"
	service.Ports = []types.ServicePortConfig{{Target: 53, Published: "53", Protocol: "udp"}}
	app.Services["app"] = service
	docker := &fakeBackupDocker{mountpoints: map[string]string{"demo_data": "/x"}}
	copier := &fakeCopier{}

	manifest, err := RunBackup(context.Background(), app, docker, copier, BackupOptions{
		Destination: "offsite", Stamp: "run", HoldStill: true,
		Containers: func(context.Context) (map[string][]codegen.ContainerSummary, error) {
			return map[string][]codegen.ContainerSummary{"app": {{ID: "c1", State: "running"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(docker.stopped) != 0 || len(docker.started) != 0 {
		t.Fatalf("a DNS server was stopped for its backup: stopped %v, started %v", docker.stopped, docker.started)
	}
	if manifest.ContainersStopped {
		t.Fatal("the manifest says the app was held still, and it was not")
	}
	if len(copier.calls) == 0 {
		t.Fatal("nothing was copied")
	}
}

// The History says what was done, not what was asked for.
func TestARunRecordSaysWhetherTheAppWasReallyStopped(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		record := BackupRunRecord{App: "demo", ContainersStopped: !stopped}
		finishBackupRecordWith(&record, BackupManifest{ContainersStopped: stopped}, nil, func(BackupRunRecord) error { return nil })
		if record.ContainersStopped != stopped {
			t.Fatalf("record says stopped=%v, the run was stopped=%v", record.ContainersStopped, stopped)
		}
	}
}
