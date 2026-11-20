package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
)

// A Disabled component with central admission must be re-sampled until the listener serves,
// because the listener is not a watched source.
func TestReconcileRequeuesWhileCentralAdmissionNotReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	wac := &v1alpha1.WorkloadAvailabilityConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.SingletonName, Finalizers: []string{Finalizer}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wac).WithStatusSubresource(wac).Build()
	ready := false
	r := &WorkloadAvailabilityConfigReconciler{Client: c, Scheme: scheme, Namespace: "wao", Images: testImages(), CentralAdmissionReady: func() bool { return ready }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: v1alpha1.SingletonName}}

	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != requeueProgressing {
		t.Fatalf("RequeueAfter = %v with central admission not ready, want %v", res.RequeueAfter, requeueProgressing)
	}

	ready = true
	res, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %v with central admission ready, want 0", res.RequeueAfter)
	}
	got := &v1alpha1.WorkloadAvailabilityConfig{}
	if err := c.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	for _, st := range got.Status.Components {
		if st.Name == v1alpha1.ComponentKeyFenceAgentsRemediation && (st.AdmissionReady == nil || !*st.AdmissionReady) {
			t.Fatalf("FAR AdmissionReady = %v after the listener serves", st.AdmissionReady)
		}
	}
}
