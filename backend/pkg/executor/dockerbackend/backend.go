// Package dockerbackend adapts the Docker sandbox client (pkg/docker) to the
// provider-neutral executor.FlowExecutor seam. It changes no behaviour: the
// Docker path a flow takes today is reproduced exactly, so EXECUTOR_BACKEND=docker
// runs identically to before the seam existed.
//
// The sandbox shape (entrypoint, capability allow-list, /work mount, DOCKER_*
// env, network, ports, pids limit) is owned by docker.WorkerSpec + RunContainer
// and derived from the Config, so RunSandbox uses only ContainerSpec.Image and
// lets the Docker layer build the rest — the ContainerSpec fields meaningful to
// the Kubernetes backend (Entrypoint/Capabilities/Env/WorkDir/Labels/PidsLimit)
// are intentionally ignored here.
package dockerbackend

import (
	"context"
	"io"
	"sync"

	"pentagi/pkg/config"
	"pentagi/pkg/database"
	"pentagi/pkg/docker"
	"pentagi/pkg/executor"

	"github.com/moby/moby/client"
)

type execMeta struct {
	tty         bool
	attachStdin bool
}

// Backend implements executor.FlowExecutor over a docker.DockerClient.
type Backend struct {
	client docker.DockerClient
	cfg    *config.Config

	mu    sync.Mutex
	execs map[string]execMeta
}

var _ executor.FlowExecutor = (*Backend)(nil)

func New(dockerClient docker.DockerClient, cfg *config.Config) *Backend {
	return &Backend{client: dockerClient, cfg: cfg, execs: make(map[string]execMeta)}
}

func (b *Backend) RunSandbox(
	ctx context.Context, name string, ctype database.ContainerType, flowID int64, spec executor.ContainerSpec,
) (database.Container, error) {
	workerConfig, workerHostConfig := docker.WorkerSpec(b.cfg, spec.Image)
	return b.client.RunContainer(ctx, name, ctype, flowID, workerConfig, workerHostConfig)
}

func (b *Backend) StopSandbox(ctx context.Context, id string, dbID int64) error {
	return b.client.StopContainer(ctx, id, dbID)
}

func (b *Backend) RemoveSandbox(ctx context.Context, id string, dbID int64) error {
	return b.client.RemoveContainer(ctx, id, dbID)
}

func (b *Backend) IsRunning(ctx context.Context, id string) (bool, error) {
	return b.client.IsContainerRunning(ctx, id)
}

func (b *Backend) KillFlowCommands(ctx context.Context, id string) error {
	return b.client.KillFlowCommands(ctx, id)
}

func (b *Backend) Exec(ctx context.Context, target string, spec executor.ExecSpec) (string, error) {
	res, err := b.client.ContainerExecCreate(ctx, target, client.ExecCreateOptions{
		Cmd:          spec.Cmd,
		Env:          spec.Env,
		WorkingDir:   spec.WorkingDir,
		TTY:          spec.TTY,
		AttachStdin:  spec.AttachStdin,
		AttachStdout: spec.AttachStdout,
		AttachStderr: spec.AttachStderr,
	})
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.execs[res.ID] = execMeta{tty: spec.TTY, attachStdin: spec.AttachStdin}
	b.mu.Unlock()
	return res.ID, nil
}

func (b *Backend) ExecAttach(ctx context.Context, execID string) (executor.ExecStream, error) {
	b.mu.Lock()
	meta := b.execs[execID]
	b.mu.Unlock()

	resp, err := b.client.ContainerExecAttach(ctx, execID, client.ExecAttachOptions{TTY: meta.tty})
	if err != nil {
		return nil, err
	}
	if meta.tty {
		return newTTYStream(resp, meta.attachStdin), nil
	}
	return newMuxStream(resp, meta.attachStdin), nil
}

func (b *Backend) ExecResult(ctx context.Context, execID string) (int, error) {
	inspect, err := b.client.ContainerExecInspect(ctx, execID)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	delete(b.execs, execID)
	b.mu.Unlock()
	return inspect.ExitCode, nil
}

func (b *Backend) StatPath(ctx context.Context, id, path string) (executor.PathStat, error) {
	st, err := b.client.ContainerStatPath(ctx, id, path)
	if err != nil {
		return executor.PathStat{}, err
	}
	return executor.PathStat{Name: st.Name, Size: st.Size, Mode: st.Mode, Mtime: st.Mtime}, nil
}

func (b *Backend) ListDir(ctx context.Context, id, dir string) (executor.DirListing, error) {
	l, err := b.client.ListContainerDir(ctx, id, dir)
	if err != nil {
		return executor.DirListing{}, err
	}
	out := executor.DirListing{Truncated: l.Truncated}
	for _, f := range l.Files {
		out.Files = append(out.Files, executor.PathStat{Name: f.Name, Size: f.Size, Mode: f.Mode, Mtime: f.Mtime})
	}
	for _, fe := range l.Failures {
		msg := ""
		if fe.Err != nil {
			msg = fe.Err.Error()
		}
		out.Failures = append(out.Failures, executor.EntryError{Name: fe.Name, Path: fe.Path, Err: msg})
	}
	return out, nil
}

func (b *Backend) CopyIn(ctx context.Context, id, dstDir string, tar io.Reader) error {
	return b.client.CopyToContainer(ctx, id, dstDir, tar, client.CopyToContainerOptions{AllowOverwriteDirWithFile: true})
}

func (b *Backend) CopyOut(ctx context.Context, id, srcPath string) (io.ReadCloser, executor.PathStat, error) {
	rc, st, err := b.client.CopyFromContainer(ctx, id, srcPath)
	if err != nil {
		return nil, executor.PathStat{}, err
	}
	return rc, executor.PathStat{Name: st.Name, Size: st.Size, Mode: st.Mode, Mtime: st.Mtime}, nil
}

// OOBPorts mirrors the Docker publishing model: host port == container port, so
// BindPort == AdvertisePort and AdvertiseHost is the configured DockerPublicIP.
func (b *Backend) OOBPorts(flowID int64) []executor.OOBPort {
	host := b.cfg.WorkerPublicIP()
	ports := executor.PrimaryContainerPorts(b.cfg.WorkerPortsBase(), flowID)
	out := make([]executor.OOBPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, executor.OOBPort{BindPort: p, AdvertiseHost: host, AdvertisePort: p})
	}
	return out
}

func (b *Backend) DefaultImage() string { return b.client.GetDefaultImage() }

func (b *Backend) Cleanup(ctx context.Context) error { return b.client.Cleanup(ctx) }
