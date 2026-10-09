// Package executor is the provider-neutral seam for running a flow's sandbox.
//
// A flow's real work happens inside a long-lived sandbox container that the
// agents exec into. Historically that container was a Docker container managed
// through pkg/docker; FlowExecutor abstracts the operations the rest of the
// backend needs so a second backend (Kubernetes, pkg/executor/k8sbackend) can
// provide the same sandbox without a host Docker socket or a privileged pod.
//
// No moby/docker or k8s types appear in this package's exported signatures:
// each backend converts to and from its own SDK types internally.
package executor

import (
	"context"
	"io"
	"os"
	"time"

	"pentagi/pkg/database"
)

// containerPortsNumber is the count of out-of-band (OOB) ports published per
// flow for reverse shells / callbacks; limit bounds the deterministic offset so
// two flows rarely collide. BaseContainerPortsNumber is the default base. These
// mirror the constants the Docker backend has used so a flow's port assignment
// is identical across backends.
const (
	containerPortsNumber      = 2
	limitContainerPortsNumber = 2000
	BaseContainerPortsNumber  = 28000

	// WorkFolderPathInContainer is where /work is mounted inside the sandbox.
	WorkFolderPathInContainer = "/work"
)

// Capabilities is a Linux capability allow-list applied on top of "drop ALL".
type Capabilities struct {
	Add []string
}

// ContainerSpec describes a sandbox container in backend-neutral terms. A
// backend maps it to its own representation (Docker container.Config/HostConfig,
// or a k8s Pod spec).
type ContainerSpec struct {
	Image        string
	Entrypoint   []string
	Env          []string
	Capabilities Capabilities
	WorkDir      string
	Labels       map[string]string
	PidsLimit    *int64
}

// ExecSpec describes one command executed inside a running sandbox.
type ExecSpec struct {
	Cmd          []string
	Env          []string
	WorkingDir   string
	TTY          bool
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
}

// ExecStream is a live exec's I/O. Stdout/Stderr are already demultiplexed (the
// Docker backend demuxes the hijacked frame stream; k8s delivers them split).
// Stdin is nil when the exec did not attach stdin.
type ExecStream interface {
	Stdout() io.Reader
	Stderr() io.Reader
	Stdin() io.WriteCloser
	Close() error
}

// PathStat is a backend-neutral stat result. It replaces moby's
// container.PathStat so callers do not depend on the Docker SDK.
type PathStat struct {
	Name  string
	Size  int64
	Mode  os.FileMode
	Mtime time.Time
}

// EntryError is a single failed entry during a directory listing.
type EntryError struct {
	Name string
	Path string
	Err  string
}

// DirListing is the result of ListDir: the entries read, per-entry failures,
// and whether the directory held more children than were returned.
type DirListing struct {
	Files     []PathStat
	Failures  []EntryError
	Truncated bool
}

// OOBPort is one published out-of-band port for a flow, describing BOTH sides
// of a reverse-shell/callback so the agent is never told an unreachable value:
//
//   - BindPort: the port a listener binds INSIDE the sandbox (0.0.0.0:BindPort).
//   - AdvertiseHost:AdvertisePort: the externally reachable address the target's
//     payload must call BACK to.
//
// Docker publishes host==container 1:1, so BindPort == AdvertisePort and
// AdvertiseHost is DockerPublicIP. Kubernetes exposes the pod through a NodePort
// Service with nodePort == targetPort == BindPort (a single number, like
// Docker), reached at the node's InternalIP (or a configured override), so
// AdvertisePort == BindPort while AdvertiseHost is the node/LB address — never
// 0.0.0.0 and never the pod IP.
type OOBPort struct {
	BindPort      int
	AdvertiseHost string
	AdvertisePort int
}

// FlowExecutor runs and drives a flow's sandbox. Implementations: the Docker
// backend (pkg/executor/dockerbackend) and the Kubernetes backend
// (pkg/executor/k8sbackend). A sandbox is long-lived (PID 1 is an idle process)
// and all work is Exec'd into it.
type FlowExecutor interface {
	RunSandbox(ctx context.Context, name string, ctype database.ContainerType, flowID int64, spec ContainerSpec) (database.Container, error)
	StopSandbox(ctx context.Context, id string, dbID int64) error
	RemoveSandbox(ctx context.Context, id string, dbID int64) error
	IsRunning(ctx context.Context, id string) (bool, error)
	KillFlowCommands(ctx context.Context, id string) error

	// Exec starts a command and returns a handle; ExecAttach streams its I/O;
	// ExecResult blocks for the exit code. The split hides that k8s has a single
	// exec call with no exec id and returns the exit code only as a stream error,
	// while Docker has three distinct calls.
	Exec(ctx context.Context, target string, spec ExecSpec) (execID string, err error)
	ExecAttach(ctx context.Context, execID string) (ExecStream, error)
	ExecResult(ctx context.Context, execID string) (exitCode int, err error)

	StatPath(ctx context.Context, id, path string) (PathStat, error)
	ListDir(ctx context.Context, id, dir string) (DirListing, error)
	CopyIn(ctx context.Context, id, dstDir string, tar io.Reader) error
	CopyOut(ctx context.Context, id, srcPath string) (io.ReadCloser, PathStat, error)

	OOBPorts(flowID int64) []OOBPort
	DefaultImage() string
	Cleanup(ctx context.Context) error
}

// PrimaryContainerPorts returns the OOB host ports for a flow relative to
// portsBase. A portsBase outside the usable range falls back to
// BaseContainerPortsNumber. The assignment is deterministic in flowID so a
// restored flow keeps its ports; it matches the Docker backend exactly.
func PrimaryContainerPorts(portsBase int, flowID int64) []int {
	if portsBase <= 0 || portsBase > (65535-limitContainerPortsNumber) {
		portsBase = BaseContainerPortsNumber
	}
	ports := make([]int, containerPortsNumber)
	for i := range containerPortsNumber {
		delta := (int(flowID)*containerPortsNumber + i) % limitContainerPortsNumber
		ports[i] = portsBase + delta
	}
	return ports
}

// WorkerCapabilities is the allow-list that pairs with "drop ALL": Docker's
// default set minus MKNOD (block-device escape), plus SYS_PTRACE, plus
// NET_ADMIN when netAdmin is set. Never add SYS_ADMIN, SYS_MODULE, SYS_RAWIO,
// SYS_BOOT.
func WorkerCapabilities(netAdmin bool) []string {
	capAdd := []string{
		"CHOWN", "DAC_OVERRIDE", "FSETID", "FOWNER",
		"NET_RAW", "SETGID", "SETUID", "SETFCAP", "SETPCAP",
		"NET_BIND_SERVICE", "SYS_CHROOT", "KILL", "AUDIT_WRITE", "SYS_PTRACE",
	}
	if netAdmin {
		capAdd = append(capAdd, "NET_ADMIN")
	}
	return capAdd
}
