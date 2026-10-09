package dockerbackend

import (
	"testing"

	"pentagi/pkg/config"
	"pentagi/pkg/executor"
)

func TestBackend_OOBPorts_BindEqualsAdvertiseOnPublicIP(t *testing.T) {
	b := &Backend{cfg: &config.Config{DockerPublicIP: "10.1.2.3", DockerPortsBase: 28000}}

	ports := b.OOBPorts(7)
	want := executor.PrimaryContainerPorts(28000, 7)
	if len(ports) != len(want) {
		t.Fatalf("got %d ports want %d", len(ports), len(want))
	}
	for i, p := range ports {
		if p.BindPort != want[i] || p.AdvertisePort != want[i] {
			t.Fatalf("port %d: bind=%d advertise=%d want both %d", i, p.BindPort, p.AdvertisePort, want[i])
		}
		if p.AdvertiseHost != "10.1.2.3" {
			t.Fatalf("port %d: advertiseHost=%q want 10.1.2.3", i, p.AdvertiseHost)
		}
	}
}
