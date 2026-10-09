package k8sbackend

import (
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type corev1client = typedcorev1.PodInterface

func isNotFound(err error) bool      { return apierrors.IsNotFound(err) }
func isAlreadyExists(err error) bool { return apierrors.IsAlreadyExists(err) }
