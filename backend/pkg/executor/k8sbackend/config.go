package k8sbackend

// Config parameterizes the Kubernetes sandbox backend. It is populated from the
// global pkg/config by the cmd/pentagi wiring; the backend keeps its own struct
// so it does not import pkg/config and stays unit-testable.
type Config struct {
	// Namespace is where per-flow sandbox Pods and OOB Services are created.
	Namespace string
	// DefaultImage is the sandbox image used when a flow has none.
	DefaultImage string
	// ImagePullSecret, when set, is added to the Pod's imagePullSecrets.
	ImagePullSecret string
	// NetAdmin adds the NET_ADMIN capability to the sandbox (mirrors Docker's
	// DockerNetAdmin).
	NetAdmin bool

	// OOBPortBase is the base of the deterministic 2-ports-per-flow OOB range.
	// It defaults to the NodePort range (30000) so no apiserver
	// --service-node-port-range change is needed; set it to 28000 (and widen the
	// range cluster-side) to match the Docker backend's base.
	OOBPortBase int
	// OOBAdvertiseHost overrides the callback host the agent is told. Empty means
	// resolve the scheduling node's InternalIP. Set it to a stable NLB/ENI/DNS
	// name when the node IP is not the right reverse-shell callback address.
	OOBAdvertiseHost string

	// PodNamePrefix prefixes per-flow sandbox Pod/Service names.
	PodNamePrefix string
}

const (
	defaultPodNamePrefix = "pentagi-flow"
	defaultOOBPortBase   = 30000
	defaultSandboxImage  = "debian:latest"
)

func (c Config) withDefaults() Config {
	if c.PodNamePrefix == "" {
		c.PodNamePrefix = defaultPodNamePrefix
	}
	if c.OOBPortBase == 0 {
		c.OOBPortBase = defaultOOBPortBase
	}
	if c.DefaultImage == "" {
		c.DefaultImage = defaultSandboxImage
	}
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	return c
}
