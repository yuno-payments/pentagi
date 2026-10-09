package k8sbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"pentagi/pkg/executor"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// pendingExec is an exec created by Exec and consumed by ExecAttach/ExecResult.
// k8s has a single exec call (no exec id, no inspect) so the backend caches the
// request between the three FlowExecutor calls and parses the exit code from the
// stream error.
type pendingExec struct {
	ctx    context.Context
	target string
	spec   executor.ExecSpec

	done chan struct{}
	err  error
}

func newExecID() string {
	var b [12]byte
	for i := range b {
		b[i] = byte('a' + (i*7+3)%26)
	}
	return "exec-" + string(b[:])
}

func (b *Backend) Exec(ctx context.Context, target string, spec executor.ExecSpec) (string, error) {
	id := fmt.Sprintf("%s-%d", newExecID(), len(b.execs))
	b.mu.Lock()
	b.execs[id] = &pendingExec{ctx: ctx, target: target, spec: spec, done: make(chan struct{})}
	b.mu.Unlock()
	return id, nil
}

func (b *Backend) ExecAttach(_ context.Context, execID string) (executor.ExecStream, error) {
	b.mu.Lock()
	pe := b.execs[execID]
	b.mu.Unlock()
	if pe == nil {
		return nil, fmt.Errorf("unknown exec id %q", execID)
	}

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	var stdinR *io.PipeReader
	var stdinW *io.PipeWriter
	if pe.spec.AttachStdin {
		stdinR, stdinW = io.Pipe()
	}

	go func() {
		var in io.Reader
		if stdinR != nil {
			in = stdinR
		}
		err := b.streamExec(pe.ctx, pe.target, pe.spec, in, outW, errW)
		outW.CloseWithError(err)
		errW.CloseWithError(err)
		pe.err = err
		close(pe.done)
	}()

	return &k8sExecStream{stdout: outR, stderr: errR, stdin: stdinW}, nil
}

func (b *Backend) ExecResult(_ context.Context, execID string) (int, error) {
	b.mu.Lock()
	pe := b.execs[execID]
	b.mu.Unlock()
	if pe == nil {
		return -1, fmt.Errorf("unknown exec id %q", execID)
	}
	<-pe.done
	return parseExecExitCode(pe.err)
}

// streamExec runs one command in the sandbox container via the pod exec
// subresource. stdout/stderr arrive already split (unlike Docker's multiplexed
// frame stream).
func (b *Backend) streamExec(ctx context.Context, target string, spec executor.ExecSpec, stdin io.Reader, stdout, stderr io.Writer) error {
	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(target).Namespace(b.cfg.Namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: sandboxContainerName,
		Command:   spec.Cmd,
		Stdin:     stdin != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       spec.TTY,
	}, scheme.ParameterCodec)

	exec, err := b.newExec(b.restConfig, "POST", req.URL())
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    spec.TTY,
	})
}

// execCapture runs a command to completion and returns stdout, stderr and the
// exit code. Used by StatPath/ListDir/CopyOut where the backend needs the output
// synchronously.
func (b *Backend) execCapture(ctx context.Context, target string, cmd []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	var outBuf, errBuf bytes.Buffer
	runErr := b.streamExec(ctx, target, executor.ExecSpec{Cmd: cmd}, stdin, &outBuf, &errBuf)
	code, codeErr := parseExecExitCode(runErr)
	if codeErr != nil {
		return outBuf.Bytes(), errBuf.Bytes(), -1, codeErr
	}
	return outBuf.Bytes(), errBuf.Bytes(), code, nil
}

// parseExecExitCode turns a remotecommand stream error into an exit code. A nil
// error is exit 0; a non-zero command exit arrives as utilexec.CodeExitError (or
// an apimachinery status carrying "exit code N"); anything else is a transport
// error returned as-is with code -1.
func parseExecExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var cee utilexec.CodeExitError
	if errors.As(err, &cee) {
		return cee.Code, nil
	}
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	if code, ok := exitCodeFromStatusMessage(err.Error()); ok {
		return code, nil
	}
	return -1, err
}

// exitCodeFromStatusMessage extracts N from "command terminated with non-zero
// exit code ... exit code N" style apimachinery status messages.
func exitCodeFromStatusMessage(msg string) (int, bool) {
	const marker = "exit code "
	i := lastIndex(msg, marker)
	if i < 0 {
		return 0, false
	}
	rest := msg[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0, false
	}
	return n, true
}

func lastIndex(s, sub string) int {
	return bytes.LastIndex([]byte(s), []byte(sub))
}

// KillFlowCommands sweeps the pod's processes (the pod is the PID namespace),
// leaving PID 1 (the idle sandbox entrypoint) alive.
func (b *Backend) KillFlowCommands(ctx context.Context, id string) error {
	_, _, _, err := b.execCapture(ctx, id, []string{
		"sh", "-c", "kill -TERM -1 2>/dev/null; true",
	}, nil)
	return err
}

type k8sExecStream struct {
	stdout *io.PipeReader
	stderr *io.PipeReader
	stdin  *io.PipeWriter
	once   sync.Once
}

func (s *k8sExecStream) Stdout() io.Reader { return s.stdout }
func (s *k8sExecStream) Stderr() io.Reader { return s.stderr }
func (s *k8sExecStream) Stdin() io.WriteCloser {
	if s.stdin == nil {
		return nil
	}
	return s.stdin
}
func (s *k8sExecStream) Close() error {
	s.once.Do(func() {
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		_ = s.stdout.Close()
		_ = s.stderr.Close()
	})
	return nil
}
