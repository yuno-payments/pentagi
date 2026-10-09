package k8sbackend

import (
	"testing"

	"pentagi/pkg/executor"

	corev1 "k8s.io/api/core/v1"
)

func TestBackend_BuildPod_DropsAllCapsAndForbidsPrivilege(t *testing.T) {
	b := newTestBackend(Config{Namespace: "pentest", ImagePullSecret: "ecr-pull"})
	pod := b.buildPod(5, executor.ContainerSpec{
		Image:        "debian:latest",
		Capabilities: executor.Capabilities{Add: executor.WorkerCapabilities(false)},
	})

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.Privileged == nil || *sc.Privileged {
		t.Fatalf("privileged must be explicitly false")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("allowPrivilegeEscalation must be explicitly false")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("must drop ALL, got %v", sc.Capabilities.Drop)
	}
	for _, cap := range sc.Capabilities.Add {
		if cap == "SYS_ADMIN" || cap == "MKNOD" {
			t.Fatalf("forbidden capability %q added", cap)
		}
	}
	if len(pod.Spec.ImagePullSecrets) != 1 || pod.Spec.ImagePullSecrets[0].Name != "ecr-pull" {
		t.Fatalf("imagePullSecret not set from config")
	}
}

func TestBackend_BuildPod_InjectsNodeNameViaDownwardAPIAndWorkEmptyDir(t *testing.T) {
	b := newTestBackend(Config{Namespace: "pentest"})
	pod := b.buildPod(5, executor.ContainerSpec{})

	var nodeEnv *corev1.EnvVar
	for i := range pod.Spec.Containers[0].Env {
		if pod.Spec.Containers[0].Env[i].Name == nodeNameEnv {
			nodeEnv = &pod.Spec.Containers[0].Env[i]
		}
	}
	if nodeEnv == nil || nodeEnv.ValueFrom == nil || nodeEnv.ValueFrom.FieldRef == nil ||
		nodeEnv.ValueFrom.FieldRef.FieldPath != "spec.nodeName" {
		t.Fatalf("node name must be injected via downward API spec.nodeName")
	}
	foundWork := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == "work" && v.EmptyDir != nil {
			foundWork = true
		}
	}
	if !foundWork {
		t.Fatalf("/work must be an emptyDir volume")
	}
	if len(pod.Spec.Containers[0].Command) == 0 || pod.Spec.Containers[0].Command[0] != "tail" {
		t.Fatalf("default entrypoint must be the idle tail")
	}
}
