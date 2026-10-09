package k8sbackend

import (
	"context"
	"fmt"
	"time"

	"pentagi/pkg/executor"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// oobServiceName / oobServiceNameForPod name a flow's OOB Service. The pod form
// derives the flow id from the pod name so RemoveSandbox can delete the Service
// without carrying the flow id.
func (b *Backend) oobServiceName(flowID int64) string {
	return fmt.Sprintf("%s-%d-oob", b.cfg.PodNamePrefix, flowID)
}

func (b *Backend) oobServiceNameForPod(podName string) string {
	return podName + "-oob"
}

// oobBindPorts are the deterministic per-flow ports, rebased into the NodePort
// range via cfg.OOBPortBase. Each is used as nodePort == targetPort == the port
// a listener binds INSIDE the pod, so the agent sees ONE port number (as with
// Docker) while the reachable host differs (the node IP, not 0.0.0.0).
func (b *Backend) oobBindPorts(flowID int64) []int {
	return executor.PrimaryContainerPorts(b.cfg.OOBPortBase, flowID)
}

// ensureOOBService creates the per-flow NodePort Service. nodePort, targetPort
// and the pod listener port are all the SAME number so a reverse shell binds and
// is reached on one port; only the host (node IP) differs from the in-pod bind
// address.
func (b *Backend) ensureOOBService(ctx context.Context, flowID int64) error {
	ports := b.oobBindPorts(flowID)
	svcPorts := make([]corev1.ServicePort, 0, len(ports))
	for i, p := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       fmt.Sprintf("oob-%d", i),
			Protocol:   corev1.ProtocolTCP,
			Port:       int32(p),
			TargetPort: intstr.FromInt(p),
			NodePort:   int32(p),
		})
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.oobServiceName(flowID),
			Namespace: b.cfg.Namespace,
			Labels:    b.flowLabels(flowID),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: b.flowLabels(flowID),
			Ports:    svcPorts,
		},
	}
	_, err := b.clientset.CoreV1().Services(b.cfg.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

// OOBPorts returns, per flow port, the in-pod bind port and the externally
// reachable advertiseHost:advertisePort a target's payload must call back to.
// advertisePort == bindPort (nodePort==targetPort). advertiseHost is the
// configured override, else the scheduling node's InternalIP.
func (b *Backend) OOBPorts(flowID int64) []executor.OOBPort {
	ports := b.oobBindPorts(flowID)
	host := b.resolveAdvertiseHost(flowID)
	out := make([]executor.OOBPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, executor.OOBPort{
			BindPort:      p,
			AdvertiseHost: host,
			AdvertisePort: p,
		})
	}
	return out
}

// resolveAdvertiseHost returns the OOB callback host: the configured override,
// else the InternalIP of the node the flow's Pod is scheduled on. Empty string
// when it cannot be resolved (the caller/prompt surfaces that honestly rather
// than advertising a wrong address).
func (b *Backend) resolveAdvertiseHost(flowID int64) string {
	if b.cfg.OOBAdvertiseHost != "" {
		return b.cfg.OOBAdvertiseHost
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pod, err := b.pods().Get(ctx, b.podName(flowID), metav1.GetOptions{})
	if err != nil || pod.Spec.NodeName == "" {
		return ""
	}
	node, err := b.clientset.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}
