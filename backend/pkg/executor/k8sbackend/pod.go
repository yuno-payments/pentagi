package k8sbackend

import (
	"fmt"

	"pentagi/pkg/executor"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	sandboxContainerName = "sandbox"
	// nodeNameEnv is injected via the downward API so the backend can resolve the
	// scheduling node's InternalIP for OOB callback advertisement.
	nodeNameEnv = "PENTAGI_NODE_NAME"
	// labelFlowID / labelManagedBy scope a flow's objects for Cleanup and lookup.
	labelFlowID    = "pentagi.io/flow-id"
	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "pentagi"
)

func (b *Backend) podName(flowID int64) string {
	return fmt.Sprintf("%s-%d", b.cfg.PodNamePrefix, flowID)
}

func (b *Backend) flowLabels(flowID int64) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelFlowID:    fmt.Sprintf("%d", flowID),
	}
}

// buildPod renders the sandbox Pod: an idle PID 1 the agents exec into, caps
// dropped to the allow-list, no privilege escalation, non-root-friendly, /work
// on an emptyDir, and the node name exposed via the downward API. No host
// Docker socket and no privileged flag — the k8s backend never shims DinD.
func (b *Backend) buildPod(flowID int64, spec executor.ContainerSpec) *corev1.Pod {
	image := spec.Image
	if image == "" {
		image = b.cfg.DefaultImage
	}
	entrypoint := spec.Entrypoint
	if len(entrypoint) == 0 {
		entrypoint = []string{"tail", "-f", "/dev/null"}
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = executor.WorkFolderPathInContainer
	}

	env := []corev1.EnvVar{{
		Name:      nodeNameEnv,
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}},
	}}
	for _, e := range spec.Env {
		if k, v, ok := splitEnv(e); ok {
			env = append(env, corev1.EnvVar{Name: k, Value: v})
		}
	}

	labels := b.flowLabels(flowID)
	for k, v := range spec.Labels {
		labels[k] = v
	}

	noEscalate := false
	privileged := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.podName(flowID),
			Namespace: b.cfg.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyOnFailure,
			Containers: []corev1.Container{{
				Name:       sandboxContainerName,
				Image:      image,
				Command:    entrypoint,
				Env:        env,
				WorkingDir: workDir,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "work",
					MountPath: workDir,
				}},
				SecurityContext: &corev1.SecurityContext{
					Privileged:               &privileged,
					AllowPrivilegeEscalation: &noEscalate,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
						Add:  toCapabilities(spec.Capabilities.Add),
					},
				},
			}},
			Volumes: []corev1.Volume{{
				Name:         "work",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		},
	}
	if b.cfg.ImagePullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: b.cfg.ImagePullSecret}}
	}
	return pod
}

func toCapabilities(xs []string) []corev1.Capability {
	out := make([]corev1.Capability, 0, len(xs))
	for _, x := range xs {
		out = append(out, corev1.Capability(x))
	}
	return out
}

func splitEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
