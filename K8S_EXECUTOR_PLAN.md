# K8s Executor Plan — PentAGI container-execution seam (GROUNDWORK / PROPOSAL)

Status: **groundwork + proposal only**. No `.go` files are changed by this document.
Branch: `feature/paas-k8s-executor`. Baseline (`main`): `go build ./...` is clean on go 1.26.0 (module declares `go 1.26.5`, `GOTOOLCHAIN=auto`).

Goal: replace the Docker-based sandbox/tool execution with a Kubernetes backend that
creates Pods/Jobs through the k8s API (client-go), authorized by a ServiceAccount+RBAC.
Hard constraints: **no privileged pod, no host Docker socket bind-mount, no DinD.**

---

## 1. The seam today

All container execution goes through a single Go interface, `docker.DockerClient`
(`backend/pkg/docker/client.go:95`), built by `docker.NewDockerClient`
(`backend/pkg/docker/client.go:133`). Every consumer holds the interface, never the
concrete `*dockerClient`, so the seam is already an interface boundary — the refactor is
to introduce a provider-neutral name and a second implementation, not to invent a seam.

Caveat: the interface **leaks moby SDK types** in its signatures
(`container.Config`, `container.HostConfig`, `container.PathStat`,
`client.ExecCreateOptions`, `client.ExecAttachOptions`, `client.ExecCreateResult`,
`client.HijackedResponse`, `client.ExecInspectResult`, `client.CopyToContainerOptions`).
A k8s backend cannot satisfy those as-is. The proposed `FlowExecutor` (section 4) replaces
them with provider-neutral value types; the Docker backend adapts them back to moby types
internally.

Vendor note: this fork imports `github.com/moby/moby/...` and `github.com/moby/moby/client`
(NOT `github.com/docker/docker/...`) and `github.com/containerd/errdefs`.

---

## 2. Verbatim `DockerClient` interface (backend/pkg/docker/client.go:95)

```go
type DockerClient interface {
	RunContainer(ctx context.Context, containerName string, containerType database.ContainerType,
		flowID int64, config *container.Config, hostConfig *container.HostConfig) (database.Container, error)
	StopContainer(ctx context.Context, containerID string, dbID int64) error
	RemoveContainer(ctx context.Context, containerID string, dbID int64) error
	IsContainerRunning(ctx context.Context, containerID string) (bool, error)
	KillFlowCommands(ctx context.Context, containerID string) error
	ContainerExecCreate(ctx context.Context, container string, config client.ExecCreateOptions) (client.ExecCreateResult, error)
	ContainerExecAttach(ctx context.Context, execID string, config client.ExecAttachOptions) (client.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (client.ExecInspectResult, error)
	ContainerStatPath(ctx context.Context, containerID string, path string) (container.PathStat, error)
	ListContainerDir(ctx context.Context, containerID string, dirPath string) (ContainerDirListing, error)
	CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader, options client.CopyToContainerOptions) error
	CopyFromContainer(ctx context.Context, containerID string, srcPath string) (io.ReadCloser, container.PathStat, error)
	Cleanup(ctx context.Context) error
	GetDefaultImage() string
}
```

### Constructor (backend/pkg/docker/client.go:133)

```go
func NewDockerClient(ctx context.Context, db database.Querier, cfg *config.Config) (DockerClient, error)
```

What the constructor does (summary, for porting to a k8s constructor):
- `client.New(client.FromEnv)` + `cli.Info()` to reach the daemon and learn its ID.
- Resolves the host docker socket to bind-mount into workers (`cfg.WorkerDockerSocket()` /
  autodetect) — **this is exactly the DinD/host-socket path the k8s backend must NOT have.**
- Resolves `dataDir` (abs, `MkdirAll`) and `hostDir` (`getHostDataDir`) — the host path bind-
  mounted at `/work` inside each worker.
- `ensureDockerNetwork(ctx, cli, netName)` — pre-creates the configured docker network.
- Reads tenant labels (`cfg.TenantLabels()`), inside-env (`cfg.WorkerDockerEnv()`),
  cert path, `cfg.DockerPortsBase`, `cfg.DockerPublicIP`, `cfg.DockerNetwork`.
- Calls `dockerCli.decideSandbox(ctx, cfg)` — a startup probe that launches a throwaway
  worker (identical to a flow worker via `WorkerSpec`) to decide sandbox behavior.

---

## 3. Call-site map (file:line → method)

Consumers hold `docker.DockerClient` in these struct fields / constructor params:
`pkg/tools/terminal.go:55,65`, `pkg/tools/tools.go:184,369`, `pkg/providers/providers.go:146,192`,
`pkg/server/router.go:122`, `pkg/server/services/flow_files.go:61,69`,
`pkg/controller/flow.go:77,96`, `pkg/controller/flows.go:132,150`,
`cmd/ftester/worker/executor.go:90,107`, `cmd/ftester/worker/tester.go:32,53`.
Constructed at `cmd/pentagi/main.go:221` and `cmd/ftester/main.go:127`.

### Method invocations (non-test)

| file:line | method |
|---|---|
| `pkg/tools/terminal.go:228` | `ContainerExecCreate` |
| `pkg/tools/terminal.go:270` | `ContainerExecAttach` |
| `pkg/tools/terminal.go:330` | `ContainerExecInspect` |
| `pkg/tools/terminal.go:356` | `IsContainerRunning` |
| `pkg/tools/terminal.go:441` | `CopyFromContainer` |
| `pkg/tools/terminal.go:552` | `CopyToContainer` |
| `pkg/tools/tools.go:488` | `IsContainerRunning` |
| `pkg/tools/tools.go:512` | `RemoveContainer` |
| `pkg/tools/tools.go:524` | `RunContainer` (primary per-flow sandbox) |
| `pkg/tools/tools.go:650` | `ContainerExecCreate` |
| `pkg/tools/tools.go:659` | `ContainerExecAttach` |
| `pkg/tools/tools.go:668` | `ContainerExecInspect` |
| `pkg/tools/tools.go:704` | `CopyToContainer` |
| `pkg/tools/tools.go:757` | `RemoveContainer` |
| `pkg/providers/providers.go:316` | `GetDefaultImage` |
| `pkg/providers/providers.go:321` | `GetDefaultImage` |
| `pkg/providers/helpers.go:886` | `docker.GetPrimaryContainerPorts` (pkg fn, OOB port description) |
| `pkg/server/services/flow_files.go:745` | `IsContainerRunning` |
| `pkg/server/services/flow_files.go:767` | `CopyFromContainer` |
| `pkg/server/services/flow_files.go:996` | `IsContainerRunning` |
| `pkg/server/services/flow_files.go:1057` | `ContainerStatPath` |
| `pkg/server/services/flow_files.go:1077` | `ListContainerDir` |
| `pkg/server/services/flow_files.go:1277` | `IsContainerRunning` |
| `pkg/server/services/flow_files.go:1288` | `CopyToContainer` |
| `pkg/server/services/flow_files.go:1356` | `IsContainerRunning` |
| `pkg/server/services/flow_files.go:1371` | `ContainerExecCreate` |
| `pkg/server/services/flow_files.go:1380` | `ContainerExecAttach` |
| `pkg/server/services/flow_files.go:1390` | `ContainerExecInspect` |
| `pkg/controller/flow.go:782` | `IsContainerRunning` |
| `pkg/controller/flow.go:798` | `CopyToContainer` |
| `pkg/controller/flow.go:912` | `KillFlowCommands` |
| `pkg/controller/flow_finish.go:133` | `RemoveContainer` |

`StopContainer` and `Cleanup` have no non-test callers in `pkg`/`cmd` today (part of the
interface, exercised by tests / lifecycle wiring).

Direct moby SDK imports outside `pkg/docker` (these files construct moby option structs and
must be touched when the signatures go provider-neutral):
`pkg/tools/terminal.go:22`, `pkg/tools/tools.go:28`, `pkg/server/services/flow_files.go:32-33`,
`pkg/controller/flow.go:30`, plus the installer (`cmd/installer/...`) which talks to Docker
directly and is **out of scope** for the executor abstraction.

---

## 4. How the per-flow sandbox / exec / copy / OOB works today

### Sandbox container creation (`RunContainer`, client.go:242)
The primary per-flow sandbox is created in `flowToolsExecutor.Prepare`
(`pkg/tools/tools.go:481`, `RunContainer` call at `:524`). The spec comes from
`docker.WorkerSpec(cfg, image)` (`pkg/docker/worker_spec.go:17`):
- `container.Config{ Image: image, Entrypoint: ["tail","-f","/dev/null"] }` — PID 1 is an
  idle `tail`, so the pod/container is a long-lived shell host; all real work is `exec` into it.
- `container.HostConfig{ CapDrop: ["ALL"], CapAdd: workerCapabilities(cfg) }` — explicit
  capability allow-list (Docker default 14 minus MKNOD, plus SYS_PTRACE, plus NET_ADMIN when
  `cfg.DockerNetAdmin`).

`RunContainer` then (client.go:242-460):
- Creates a DB row (`db.CreateContainer`, status `starting`), pulls the image (with a
  fallback to `defImage` on failure), sets `WorkingDir=/work` and a crc32 hostname.
- Mounts `/work`: a host bind `hostDir/flow-<id>:/work`, or a named local volume
  `<name>-data:/work` when no host dir (`:349-363`).
- Applies tenant labels, `RestartPolicy=on-failure/5`, `PidsLimit=2048`, json-file logging.
- `applyWorkerDockerAccess` (client.go:220) — injects the DOCKER_* env + cert mount + socket
  access (the DinD seam).
- Network + ports (see OOB below), then `ContainerCreate` with conflict-retry on name reuse,
  then `ensureContainerStarted` (startup grace check, client.go:562).

### Exec (`ContainerExecCreate` → `Attach` → `Inspect`)
Thin wrappers over the moby client (client.go:998-1020):
- `ExecCreate(ctx, container, ExecCreateOptions)` → returns an exec ID.
- `ExecAttach(ctx, execID, ExecAttachOptions)` → returns a `HijackedResponse` (a hijacked
  bidirectional stream: `.Reader` for multiplexed stdout/stderr, `.Conn` for stdin).
- `ExecInspect(ctx, execID)` → returns exit code.
Non-TTY exec output is a multiplexed frame stream (8-byte header: stream id + big-endian
size); the package demuxes it manually in `demuxExecStdout` (client.go:1200). Callers:
terminal command run (`terminal.go:228/270/330`), tools exec (`tools.go:650/659/668`),
directory listing (`ListContainerDir`, client.go:1078), file read exec (`flow_files.go:1371`).

### File copy (`CopyToContainer` / `CopyFromContainer`, client.go:1241-1262)
- `CopyToContainer`: writes a **tar stream** (`content io.Reader`) to a destination dir in the
  container (moby `CopyToContainer`). Callers tar up their payload first
  (`terminal.go:552`, `tools.go:704`, `flow_files.go:1288`, `controller/flow.go:798`).
- `CopyFromContainer`: returns an `io.ReadCloser` of a **tar stream** plus a `PathStat`
  (`terminal.go:441`, `flow_files.go:767`). Both are Docker's archive-copy API over `/work`.

### OOB reverse-shell ports (`DOCKER_PORTS_BASE`)
- `GetPrimaryContainerPorts(portsBase, flowID)` (client.go:121) returns
  `containerPortsNumber` (**2**) deterministic ports per flow:
  `portsBase + (flowID*2 + i) % 2000`. `portsBase` defaults to `BaseContainerPortsNumber`
  (28000), configured by `DOCKER_PORTS_BASE` (`config.go:65`, default 28000).
- In `RunContainer` (client.go:399-425): for bridge networking each port is published as a
  `PortBinding{HostIP: DockerPublicIP, HostPort: port}` and added to `ExposedPorts`, so the
  host port maps 1:1 into the container. For `network == "host"` (client.go:388) no bindings
  are made — the container shares the host netns directly.
- `DockerPublicIP` (`config.go:63`, default `0.0.0.0`) is the externally reachable callback
  address; surfaced to the LLM via `getContainerPortsDescription`
  (`pkg/providers/helpers.go:885`) as "OOB Attack Infrastructure" — the agent is told to use
  ONLY these ports for reverse shells / metasploit LPORT / callbacks, because only these are
  forwarded. This is the single hardest thing to reproduce under k8s (section 6).

---

## 5. Proposed provider-neutral `FlowExecutor` interface

Introduce `pkg/executor` (new package, NOT created in this groundwork) with a
provider-neutral interface that both a Docker backend (adapting to moby types internally)
and a Kubernetes backend (client-go) implement. `cmd/pentagi/main.go:221` selects the
implementation from config (e.g. `EXECUTOR_BACKEND=docker|kubernetes`).

```go
// Sketch — provider-neutral; no moby/docker or k8s types in signatures.
package executor

type ContainerSpec struct {
	Image        string
	Entrypoint   []string
	Env          []string
	Capabilities Capabilities // allow-list; k8s maps to securityContext.capabilities
	WorkDir      string       // "/work"
	Labels       map[string]string
	PidsLimit    *int64
}

type ExecSpec struct {
	Cmd          []string
	Env          []string
	TTY          bool
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
}

type ExecStream interface { // replaces client.HijackedResponse
	Stdout() io.Reader       // already-demuxed
	Stderr() io.Reader
	Stdin()  io.WriteCloser
	Close()  error
}

type PathStat struct { Name string; Size int64; Mode os.FileMode; Mtime time.Time }

type FlowExecutor interface {
	RunSandbox(ctx context.Context, name string, ctype database.ContainerType, flowID int64, spec ContainerSpec) (database.Container, error)
	StopSandbox(ctx context.Context, id string, dbID int64) error
	RemoveSandbox(ctx context.Context, id string, dbID int64) error
	IsRunning(ctx context.Context, id string) (bool, error)
	KillFlowCommands(ctx context.Context, id string) error

	Exec(ctx context.Context, target string, spec ExecSpec) (execID string, err error)
	ExecAttach(ctx context.Context, execID string) (ExecStream, error)
	ExecResult(ctx context.Context, execID string) (exitCode int, err error)

	StatPath(ctx context.Context, id, path string) (PathStat, error)
	ListDir(ctx context.Context, id, dir string) (DirListing, error)
	CopyIn(ctx context.Context, id, dstDir string, tar io.Reader) error     // tar stream in
	CopyOut(ctx context.Context, id, srcPath string) (io.ReadCloser, PathStat, error) // tar stream out

	OOBPorts(flowID int64) []OOBPort // was GetPrimaryContainerPorts + public IP
	DefaultImage() string
	Cleanup(ctx context.Context) error
}
```

The existing `docker.DockerClient` becomes a thin adapter behind `FlowExecutor` (or is
renamed), converting `ContainerSpec`→`container.Config/HostConfig`, `ExecSpec`→
`client.ExecCreateOptions`, and demuxing the hijacked stream into `ExecStream`.

---

## 6. Mapping table: DockerClient method → Kubernetes backend

| DockerClient method | Kubernetes (client-go) realization |
|---|---|
| `RunContainer` | Create a **Pod** (or a Deployment of 1) per flow in the executor namespace. `Entrypoint ["tail","-f","/dev/null"]` → container `command`. `/work` → an `emptyDir` (or a per-flow PVC) `volumeMount`. Caps → `securityContext.capabilities.{drop:[ALL],add:[...]}`, `allowPrivilegeEscalation:false`, `privileged:false`, non-root. Image pull via normal kubelet + `imagePullSecrets`. Status `starting`→`running` by watching the pod to `Ready`. DB row logic is backend-agnostic and stays. **No socket mount, no DinD.** |
| `StopContainer` | Delete the Pod with a grace period (or scale Deployment to 0). No true "stop+keep" in k8s; a stopped-but-kept sandbox maps to a PVC that survives pod deletion. |
| `RemoveContainer` | `Pods().Delete` (+ PVC delete if per-flow). Idempotent on NotFound. |
| `IsContainerRunning` | `Pods().Get` → phase `Running` and container `Ready`; NotFound → false. |
| `KillFlowCommands` | `exec` a sweep command (as today, `runFlowSweep`) via remotecommand, killing processes in the pod — PID namespace is the pod. |
| `ContainerExecCreate` + `ContainerExecAttach` + `ContainerExecInspect` | Collapse into **one** `remotecommand.NewSPDYExecutor` / WebSocket exec against the pod's `/exec` subresource with `Stdin/Stdout/Stderr`. k8s has **no separate create/attach/inspect**: there is no exec ID and **no exit code is returned by the API** — exit status must be parsed from the `metav1.Status` error on stream completion (exec exit codes come back as a `k8s.io/apimachinery` `StatusError` with a `exit code N` cause). The `FlowExecutor.Exec/ExecAttach/ExecResult` split hides this; the Docker adapter keeps its 3 calls, the k8s adapter runs one exec and caches the exit code. |
| `ContainerStatPath` | `exec` `stat`/`find` and parse (no archive-stat API in k8s). Reuse the NUL-delimited `find -print0` approach already in `ListContainerDir`. |
| `ListContainerDir` | Already exec-based (`find … -print0` + per-entry stat); **ports almost verbatim** — swap the docker exec wrappers for remotecommand exec. |
| `CopyToContainer` (tar in) | **tar-over-exec**: `exec` `tar -xf - -C <dstDir>` with the tar stream wired to stdin (this is exactly what `kubectl cp` does). No native copy API. |
| `CopyFromContainer` (tar out) | **tar-over-exec**: `exec` `tar -cf - <srcPath>` and read the tar stream from stdout; synthesize `PathStat` from a `stat` exec (the moby API returned stat in a header; k8s does not). |
| `GetPrimaryContainerPorts` / OOB publishing | Per-flow **Service** (NodePort or LoadBalancer) exposing the 2 deterministic ports; or a dedicated ingress for TCP. See section 6b — this is the hard unknown. |
| `Cleanup` | Label-selector delete of all flow pods/services/PVCs owned by the tenant (k8s labels replace the docker tenant labels). |
| `GetDefaultImage` | Config value, backend-agnostic. |

### 6b. HARDEST UNKNOWN — OOB reverse-shell port exposure under k8s

The Docker backend publishes 2 host ports per flow bound to a routable `DockerPublicIP`, so a
target machine on the internet can connect **back** into the sandbox (reverse shells, DNS
exfil, XXE/SSRF OOB callbacks). Reproducing *inbound-from-the-internet* reachability in k8s is
the central open problem:

- **NodePort Service** — simplest; exposes 2 ports on every node's IP in the 30000–32767
  range by default. Problems: (a) the range doesn't match `DOCKER_PORTS_BASE=28000`
  (needs `--service-node-port-range` widened or the port math rebased); (b) the reachable
  address is the node IP, not a stable `DockerPublicIP`; (c) on EKS the node SG/NACL must
  allow inbound on those ports from the internet — a security posture change.
- **LoadBalancer Service per flow** — gives a real external IP but is slow to provision
  (seconds–minutes), costly (one NLB per flow), and AWS-specific; flows are short-lived.
- **Shared NLB + per-flow target ports** — one NLB, deterministic port→pod mapping; needs a
  controller to wire targets. Most production-realistic but most to build.
- **`hostNetwork: true` pod** — mirrors docker `network=host` and keeps the existing
  host-network code path conceptually, but it is a **privilege/isolation regression** and
  likely violates the "no privileged" spirit; pod then binds host ports directly, colliding
  across pods on a node. Probably disallowed.
- **Egress-only fallback** — if inbound-from-internet is dropped, OOB verification that only
  needs *outbound* (DNS exfil to an attacker-controlled resolver, HTTP callbacks to an
  external collaborator like interactsh) still works; only true reverse *shells* needing an
  inbound bind are lost. The `getContainerPortsDescription` prompt text
  (`pkg/providers/helpers.go:885`) would have to be rewritten per backend to tell the agent
  what is actually reachable.

Recommendation to resolve before implementing: decide the OOB contract first (full inbound
reachability vs. outbound-only + external collaborator), because it dictates the Service
strategy, the node SG changes, and the prompt text — and it is the only part of the seam with
no clean 1:1 k8s analogue.

### Other k8s-specific notes
- **Auth**: in-cluster `rest.InClusterConfig()` + a dedicated ServiceAccount; RBAC Role
  granting `pods`, `pods/exec`, `pods/log`, `services`, `persistentvolumeclaims`
  (create/get/list/delete) in a single executor namespace. No cluster-admin.
- **Exec exit codes**: parse from `exec.StreamWithContext` returning a `*exec.CodeExitError`
  / apimachinery `StatusError`; there is no `ExecInspect`.
- **Startup grace** (`containerStartupGrace`, client.go:60): replace the "spawned then check
  still alive" heuristic with a pod `Ready` watch — cleaner in k8s.
- **Installer** (`cmd/installer/...`) talks to Docker directly for host diagnostics and is out
  of scope for this abstraction.

---

## 7. Scope boundary of this groundwork
- Done: baseline build verified, seam mapped, interface captured, call-sites enumerated, plan drafted.
- NOT done (intentionally): no `pkg/executor` package, no k8s backend, no edits to existing `.go` files.
