// Package k8sbackend runs a flow's sandbox as a Kubernetes Pod reached through
// the k8s API, with no host Docker socket, no privileged pod and no DinD. It
// implements executor.FlowExecutor.
//
// v1 scope note: tools that spawn their OWN containers via a mounted Docker
// socket (nested DinD) are NOT supported here — the sandbox runs tools as
// processes via exec, which covers the large majority of pentest tooling. A
// flow that truly needs to launch sibling containers must use the Docker
// backend.
package k8sbackend

import (
	"context"
	"database/sql"
	"net/url"
	"fmt"
	"sync"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/executor"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// execFactory builds a remotecommand executor for a POST exec request. It is a
// field so tests can inject a fake without a live apiserver.
type execFactory func(config *rest.Config, method string, url *url.URL) (remotecommand.Executor, error)

// Backend implements executor.FlowExecutor against a Kubernetes cluster.
type Backend struct {
	cfg        Config
	clientset  kubernetes.Interface
	restConfig *rest.Config
	db         database.Querier
	newExec    execFactory

	mu    sync.Mutex
	execs map[string]*pendingExec // execID -> in-flight exec state
}

// New builds a Backend from an in-cluster config. Used by cmd/pentagi when
// EXECUTOR_BACKEND=kubernetes.
func New(ctx context.Context, db database.Querier, cfg Config) (*Backend, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("k8s clientset: %w", err)
	}
	return NewWithClient(db, cfg, cs, rc), nil
}

// NewWithClient builds a Backend over an injected clientset/restConfig. The
// restConfig may be nil in tests that never exec.
func NewWithClient(db database.Querier, cfg Config, cs kubernetes.Interface, rc *rest.Config) *Backend {
	return &Backend{
		cfg:        cfg.withDefaults(),
		clientset:  cs,
		restConfig: rc,
		db:         db,
		newExec:    remotecommand.NewSPDYExecutor,
		execs:      make(map[string]*pendingExec),
	}
}

func (b *Backend) DefaultImage() string { return b.cfg.DefaultImage }

func (b *Backend) pods() corev1client {
	return b.clientset.CoreV1().Pods(b.cfg.Namespace)
}

// RunSandbox creates the per-flow sandbox Pod and its OOB NodePort Service,
// waits for the Pod to become Ready, then records the DB row. The Pod name is
// the executor "id" used by all later Exec/Stat/Copy calls.
func (b *Backend) RunSandbox(ctx context.Context, name string, ctype database.ContainerType, flowID int64, spec executor.ContainerSpec) (database.Container, error) {
	pod := b.buildPod(flowID, spec)
	if _, err := b.pods().Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return database.Container{}, fmt.Errorf("create sandbox pod: %w", err)
	}

	if err := b.ensureOOBService(ctx, flowID); err != nil {
		return database.Container{}, fmt.Errorf("create OOB service: %w", err)
	}

	if err := b.waitReady(ctx, pod.Name); err != nil {
		return database.Container{}, fmt.Errorf("sandbox pod did not become ready: %w", err)
	}

	image := spec.Image
	if image == "" {
		image = b.cfg.DefaultImage
	}
	dbContainer, err := b.db.CreateContainer(ctx, database.CreateContainerParams{
		Type:    ctype,
		Name:    name,
		Image:   image,
		Status:  database.ContainerStatusRunning,
		FlowID:  flowID,
		LocalID: sql.NullString{String: pod.Name, Valid: true},
	})
	if err != nil {
		return database.Container{}, fmt.Errorf("record container row: %w", err)
	}
	return dbContainer, nil
}

func (b *Backend) waitReady(ctx context.Context, podName string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		running, err := b.IsRunning(ctx, podName)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Backend) IsRunning(ctx context.Context, id string) (bool, error) {
	pod, err := b.pods().Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false, nil
	}
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == sandboxContainerName {
			return c.Ready, nil
		}
	}
	return false, nil
}

func (b *Backend) StopSandbox(ctx context.Context, id string, dbID int64) error {
	return b.RemoveSandbox(ctx, id, dbID)
}

// RemoveSandbox deletes the Pod and its OOB Service. Idempotent on NotFound.
func (b *Backend) RemoveSandbox(ctx context.Context, id string, dbID int64) error {
	if err := b.pods().Delete(ctx, id, metav1.DeleteOptions{}); err != nil && !isNotFound(err) {
		return err
	}
	svc := b.oobServiceNameForPod(id)
	if svc != "" {
		if err := b.clientset.CoreV1().Services(b.cfg.Namespace).Delete(ctx, svc, metav1.DeleteOptions{}); err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

// Cleanup deletes every sandbox Pod and OOB Service this backend owns.
func (b *Backend) Cleanup(ctx context.Context) error {
	sel := labelManagedBy + "=" + managedByValue
	if err := b.clientset.CoreV1().Pods(b.cfg.Namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: sel}); err != nil && !isNotFound(err) {
		return err
	}
	svcs, err := b.clientset.CoreV1().Services(b.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return err
	}
	for i := range svcs.Items {
		_ = b.clientset.CoreV1().Services(b.cfg.Namespace).Delete(ctx, svcs.Items[i].Name, metav1.DeleteOptions{})
	}
	return nil
}

// Backend implements executor.FlowExecutor.
var _ executor.FlowExecutor = (*Backend)(nil)
