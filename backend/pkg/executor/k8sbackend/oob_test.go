package k8sbackend

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newTestBackend(cfg Config, objs ...runtimeObject) *Backend {
	cs := fake.NewSimpleClientset(toRuntime(objs)...)
	return NewWithClient(nil, cfg, cs, nil)
}

func TestBackend_EnsureOOBService_MapsNodePortTargetAndListenToOneNumber(t *testing.T) {
	b := newTestBackend(Config{Namespace: "pentest", OOBPortBase: 30000})
	if err := b.ensureOOBService(context.Background(), 5); err != nil {
		t.Fatalf("ensureOOBService: %v", err)
	}
	svc, err := b.clientset.CoreV1().Services("pentest").Get(context.Background(), b.oobServiceName(5), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeNodePort {
		t.Fatalf("want NodePort, got %s", svc.Spec.Type)
	}
	want := b.oobBindPorts(5) // [30010, 30011]
	if len(svc.Spec.Ports) != len(want) {
		t.Fatalf("got %d ports, want %d", len(svc.Spec.Ports), len(want))
	}
	for i, sp := range svc.Spec.Ports {
		if int(sp.Port) != want[i] || int(sp.NodePort) != want[i] || sp.TargetPort.IntValue() != want[i] {
			t.Fatalf("port %d: port=%d nodePort=%d target=%d all must equal %d",
				i, sp.Port, sp.NodePort, sp.TargetPort.IntValue(), want[i])
		}
	}
}

func TestBackend_OOBPorts_AdvertisesNodeInternalIPAndSamePort(t *testing.T) {
	cfg := Config{Namespace: "pentest", OOBPortBase: 30000, PodNamePrefix: "pentagi-flow"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pentagi-flow-5", Namespace: "pentest"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "node-a.local"},
			{Type: corev1.NodeInternalIP, Address: "10.1.2.3"},
		}},
	}
	b := newTestBackend(cfg, pod, node)

	ports := b.OOBPorts(5)
	bind := b.oobBindPorts(5)
	if len(ports) != len(bind) {
		t.Fatalf("got %d OOB ports, want %d", len(ports), len(bind))
	}
	for i, p := range ports {
		if p.BindPort != bind[i] {
			t.Fatalf("bindPort %d want %d", p.BindPort, bind[i])
		}
		if p.AdvertisePort != p.BindPort {
			t.Fatalf("advertisePort %d must equal bindPort %d", p.AdvertisePort, p.BindPort)
		}
		if p.AdvertiseHost != "10.1.2.3" {
			t.Fatalf("advertiseHost %q must be the node InternalIP 10.1.2.3", p.AdvertiseHost)
		}
	}
}

func TestBackend_OOBPorts_HonorsAdvertiseHostOverride(t *testing.T) {
	b := newTestBackend(Config{Namespace: "pentest", OOBPortBase: 30000, OOBAdvertiseHost: "nlb.example.internal"})
	for _, p := range b.OOBPorts(5) {
		if p.AdvertiseHost != "nlb.example.internal" {
			t.Fatalf("advertiseHost %q must honor the override", p.AdvertiseHost)
		}
	}
}
