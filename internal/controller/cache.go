package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OperandCacheOptions keeps manager resources in the install namespace while
// watching DaemonSets cluster-wide for SBR configs created in other namespaces.
// Unstructured operand objects use the API server directly.
func OperandCacheOptions(namespace string) cache.Options {
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&appsv1.Deployment{}: {Namespaces: map[string]cache.Config{namespace: {}}},
		&appsv1.DaemonSet{}:  {},
		&corev1.Service{}:    {Namespaces: map[string]cache.Config{namespace: {}}},
	}}
}
