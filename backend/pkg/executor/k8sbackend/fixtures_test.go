package k8sbackend

import "k8s.io/apimachinery/pkg/runtime"

// runtimeObject lets a test seed the fake clientset with typed API objects.
type runtimeObject = runtime.Object

func toRuntime(objs []runtimeObject) []runtime.Object {
	out := make([]runtime.Object, 0, len(objs))
	out = append(out, objs...)
	return out
}
