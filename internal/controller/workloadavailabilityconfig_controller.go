package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/components"
	"github.com/medik8s/workload-availability-operator/internal/images"
	"github.com/medik8s/workload-availability-operator/internal/operands"
)

const (
	Finalizer          = "workloadavailability.medik8s.io/finalizer"
	FieldOwner         = "workload-availability-operator"
	requeueProgressing = 30 * time.Second
	// Retained objects are not watched; re-check them periodically so status stays current.
	requeueRetained = 60 * time.Second
)

// WorkloadAvailabilityConfigReconciler installs and removes operand components.
type WorkloadAvailabilityConfigReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Namespace string
	Images    images.Set
	// FARAdmissionReady reports if the local webhook listener of this replica serves FAR
	// admission. A nil function reports false.
	FARAdmissionReady func() bool
}

// RBAC for WAO's own objects. Operand RBAC is not created here: it is part of the WAO CSV
// (config/operands/<k>/overlays/rbac) and OLM creates it at install time, as OLM does for OADP's
// non-admin controller.
// +kubebuilder:rbac:groups=workloadavailability.medik8s.io,resources=workloadavailabilityconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloadavailability.medik8s.io,resources=workloadavailabilityconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadavailability.medik8s.io,resources=workloadavailabilityconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations;mutatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs;selfnoderemediations,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationconfigs;storagebasedremediations,verbs=get;list;watch
// +kubebuilder:rbac:groups=remediation.medik8s.io,resources=nodehealthchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=fence-agents-remediation.medik8s.io,resources=fenceagentsremediations,verbs=get;list;watch
// +kubebuilder:rbac:groups=machine-deletion-remediation.medik8s.io,resources=machinedeletionremediations,verbs=get;list;watch
// +kubebuilder:rbac:groups=nodemaintenance.medik8s.io,resources=nodemaintenances,verbs=get;list;watch

func (r *WorkloadAvailabilityConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	wac := &v1alpha1.WorkloadAvailabilityConfig{}
	if err := r.Get(ctx, req.NamespacedName, wac); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if wac.Name != v1alpha1.SingletonName {
		log.Info("ignoring non-singleton WorkloadAvailabilityConfig", "name", wac.Name)
		return ctrl.Result{}, nil
	}
	if !wac.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, wac)
	}
	if controllerutil.AddFinalizer(wac, Finalizer) {
		return ctrl.Result{Requeue: true}, r.Update(ctx, wac)
	}

	statuses := make([]v1alpha1.ComponentStatus, 0, len(components.All()))
	var errs []error
	progressing, retained := false, false
	for _, def := range components.All() {
		var st v1alpha1.ComponentStatus
		var err error
		if wac.Spec.StateOf(def.Key) == v1alpha1.ComponentStateEnabled {
			st, err = r.ensureEnabled(ctx, wac, def)
		} else {
			st, err = r.ensureDisabled(ctx, def)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", def.Key, err))
			st.Name, st.DesiredState = def.Key, wac.Spec.StateOf(def.Key)
			st.Phase, st.Message = v1alpha1.ComponentPhaseDegraded, err.Error()
		}
		if st.Phase == v1alpha1.ComponentPhaseProgressing {
			progressing = true
		}
		if st.Phase == v1alpha1.ComponentPhaseDisableIncomplete {
			retained = true
		}
		statuses = append(statuses, st)
	}
	joined := errors.Join(errs...)
	wac.Status.ObservedGeneration = wac.Generation
	wac.Status.InstallNamespace = r.Namespace
	wac.Status.Components = statuses
	wac.Status.ReleaseSet = nil
	for _, k := range r.Images.Sorted() {
		wac.Status.ReleaseSet = append(wac.Status.ReleaseSet, v1alpha1.ImageReference{Name: k, Image: r.Images.Get(k)})
	}
	wac.Status.Conditions = summarize(wac.Status.Conditions, statuses, joined, wac.Generation)
	if err := r.Status().Update(ctx, wac); err != nil {
		return ctrl.Result{}, err
	}
	if joined != nil {
		return ctrl.Result{}, joined
	}
	if progressing {
		return ctrl.Result{RequeueAfter: requeueProgressing}, nil
	}
	if retained {
		return ctrl.Result{RequeueAfter: requeueRetained}, nil
	}
	return ctrl.Result{}, nil
}

func (r *WorkloadAvailabilityConfigReconciler) ensureEnabled(ctx context.Context, wac *v1alpha1.WorkloadAvailabilityConfig, def components.Definition) (v1alpha1.ComponentStatus, error) {
	st := v1alpha1.ComponentStatus{Name: def.Key, DesiredState: v1alpha1.ComponentStateEnabled}
	objs, err := operands.Render(def, r.Namespace, r.Images)
	if err != nil {
		return st, err
	}
	overrides, err := wac.Spec.ManagerResourcesOf(def.Key)
	if err != nil {
		return st, fmt.Errorf("spec.components.%s.manager.resources.%w", def.Key, err)
	}
	if err := operands.ApplyManagerResources(def, objs, overrides); err != nil {
		return st, err
	}
	for _, o := range objs {
		if err := r.guardForeign(ctx, o); err != nil {
			return st, err
		}
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "WorkloadAvailabilityConfig",
			Name: wac.Name, UID: wac.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}})
		o.SetManagedFields(nil)
		// controller-runtime v0.24 deprecates client.Apply in Patch; the dependency update keeps the apply behavior unchanged.
		//nolint:staticcheck
		if err := r.Patch(ctx, o, client.Apply, client.FieldOwner(FieldOwner), client.ForceOwnership); err != nil {
			return st, fmt.Errorf("apply %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	ready := true
	for _, name := range def.Deployments {
		dep := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, dep); err != nil {
			if !apierrors.IsNotFound(err) {
				return st, err
			}
			// Just applied but not yet in the informer cache: not ready, not an error.
			ready = false
			st.Deployments = append(st.Deployments, v1alpha1.DeploymentStatus{Name: name, DesiredReplicas: 1})
			continue
		}
		ds := v1alpha1.DeploymentStatus{Name: name, DesiredReplicas: ptr.Deref(dep.Spec.Replicas, 1), ReadyReplicas: dep.Status.ReadyReplicas}
		ds.Ready = dep.Status.ObservedGeneration >= dep.Generation && ds.ReadyReplicas == ds.DesiredReplicas && dep.Status.UpdatedReplicas == ds.DesiredReplicas
		ready = ready && ds.Ready
		st.Deployments = append(st.Deployments, ds)
	}
	if len(def.Webhooks) > 0 {
		ok, err := r.admissionReady(ctx, def)
		if err != nil {
			return st, err
		}
		st.AdmissionReady = &ok
		ready = ready && ok
	}
	if def.CentralAdmission {
		ok := r.centralAdmissionReady()
		st.AdmissionReady = &ok
		ready = ready && ok
	}
	if ready {
		st.Phase = v1alpha1.ComponentPhaseAvailable
	} else {
		st.Phase = v1alpha1.ComponentPhaseProgressing
		st.Message = "waiting for operand Deployments and admission certificates"
	}
	return st, nil
}

// guardForeign refuses to take over an existing object that WAO did not create.
func (r *WorkloadAvailabilityConfigReconciler) guardForeign(ctx context.Context, desired *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.GetLabels()[operands.LabelManagedBy] != operands.ManagedByValue {
		return fmt.Errorf("%s %s/%s exists and is not managed by %s; refusing to adopt it", desired.GetKind(), desired.GetNamespace(), desired.GetName(), operands.ManagedByValue)
	}
	return nil
}

func (r *WorkloadAvailabilityConfigReconciler) admissionReady(ctx context.Context, def components.Definition) (bool, error) {
	for _, w := range def.Webhooks {
		switch w.Kind {
		case "ValidatingWebhookConfiguration":
			cfg := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			if err := r.Get(ctx, types.NamespacedName{Name: w.Name}, cfg); err != nil {
				return false, client.IgnoreNotFound(err)
			}
			for _, h := range cfg.Webhooks {
				if len(h.ClientConfig.CABundle) == 0 {
					return false, nil
				}
			}
		case "MutatingWebhookConfiguration":
			cfg := &admissionregistrationv1.MutatingWebhookConfiguration{}
			if err := r.Get(ctx, types.NamespacedName{Name: w.Name}, cfg); err != nil {
				return false, client.IgnoreNotFound(err)
			}
			for _, h := range cfg.Webhooks {
				if len(h.ClientConfig.CABundle) == 0 {
					return false, nil
				}
			}
		default:
			return false, fmt.Errorf("unknown webhook kind %q", w.Kind)
		}
	}
	return true, nil
}

// centralAdmissionReady reports the admission that the WAO manager serves for a component.
// That admission does not depend on the component state.
func (r *WorkloadAvailabilityConfigReconciler) centralAdmissionReady() bool {
	return r.FARAdmissionReady != nil && r.FARAdmissionReady()
}

func (r *WorkloadAvailabilityConfigReconciler) ensureDisabled(ctx context.Context, def components.Definition) (v1alpha1.ComponentStatus, error) {
	st := v1alpha1.ComponentStatus{Name: def.Key, DesiredState: v1alpha1.ComponentStateDisabled, Phase: v1alpha1.ComponentPhaseDisabled}
	objs, err := operands.Render(def, r.Namespace, r.Images)
	if err != nil {
		return st, err
	}
	var skipped []string
	for i := len(objs) - 1; i >= 0; i-- { // webhooks first, service accounts last
		o := objs[i]
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(o.GroupVersionKind())
		err := r.Get(ctx, client.ObjectKeyFromObject(o), existing)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return st, err
		}
		if existing.GetLabels()[operands.LabelManagedBy] != operands.ManagedByValue {
			skipped = append(skipped, fmt.Sprintf("%s %s/%s", o.GetKind(), o.GetNamespace(), o.GetName()))
			continue
		}
		if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return st, fmt.Errorf("delete %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	retained, err := r.retainedResources(ctx, def)
	if err != nil {
		return st, err
	}
	st.RetainedResources = retained
	var msgs []string
	if len(retained) > 0 {
		st.Phase = v1alpha1.ComponentPhaseDisableIncomplete
		msgs = append(msgs, "manager removed; agents, configs or native objects it would reconcile remain and need the component's own procedure")
	}
	if len(skipped) > 0 {
		msgs = append(msgs, fmt.Sprintf("left foreign objects untouched: %v", skipped))
	}
	if len(def.Webhooks) > 0 {
		st.AdmissionReady = ptr.To(false)
		msgs = append(msgs, "admission for this component's APIs is unavailable while Disabled")
	}
	if def.CentralAdmission {
		// WAO continues to serve admission for this component while it is Disabled.
		st.AdmissionReady = ptr.To(r.centralAdmissionReady())
	}
	if len(msgs) > 0 {
		st.Message = fmt.Sprint(msgs)
	}
	return st, nil
}

// retainedResources lists what outlives a manager-level disable: agent DaemonSets owned by
// component configs and those configs (install namespace), plus native objects of the
// component's managed kinds anywhere in the cluster.
func (r *WorkloadAvailabilityConfigReconciler) retainedResources(ctx context.Context, def components.Definition) ([]string, error) {
	var out []string
	if len(def.AgentOwners) > 0 {
		kinds := map[string]bool{}
		for _, a := range def.AgentOwners {
			kinds[a.Kind] = true
		}
		dsl := &appsv1.DaemonSetList{}
		if err := r.List(ctx, dsl, client.InNamespace(r.Namespace)); err != nil {
			return nil, err
		}
		for _, ds := range dsl.Items {
			for _, ref := range ds.OwnerReferences {
				if kinds[ref.Kind] {
					out = append(out, fmt.Sprintf("apps/v1 DaemonSet %s/%s", ds.Namespace, ds.Name))
					break
				}
			}
		}
	}
	list := func(k components.ResourceKind, opts ...client.ListOption) error {
		l := &unstructured.UnstructuredList{}
		l.SetGroupVersionKind(schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind + "List"})
		if err := r.List(ctx, l, opts...); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) { // CRD absent: nothing retained
				return nil
			}
			return err
		}
		for _, item := range l.Items {
			name := item.GetName()
			if item.GetNamespace() != "" {
				name = item.GetNamespace() + "/" + name
			}
			out = append(out, fmt.Sprintf("%s/%s %s %s", k.Group, k.Version, k.Kind, name))
		}
		return nil
	}
	for _, a := range def.AgentOwners {
		if err := list(a, client.InNamespace(r.Namespace)); err != nil {
			return nil, err
		}
	}
	for _, m := range def.ManagedKinds {
		if err := list(m); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r *WorkloadAvailabilityConfigReconciler) finalize(ctx context.Context, wac *v1alpha1.WorkloadAvailabilityConfig) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(wac, Finalizer) {
		return ctrl.Result{}, nil
	}
	log := logf.FromContext(ctx)
	for _, def := range components.All() {
		st, err := r.ensureDisabled(ctx, def)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(st.RetainedResources) > 0 {
			log.Info("config deleted with retained component resources", "component", def.Key, "retained", st.RetainedResources)
		}
	}
	controllerutil.RemoveFinalizer(wac, Finalizer)
	return ctrl.Result{}, r.Update(ctx, wac)
}

// SetupWithManager registers the controller.
func (r *WorkloadAvailabilityConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toSingleton := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: v1alpha1.SingletonName}}}
	})
	inNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetNamespace() == r.Namespace })
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkloadAvailabilityConfig{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&admissionregistrationv1.ValidatingWebhookConfiguration{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Watches(&appsv1.DaemonSet{}, toSingleton, builder.WithPredicates(inNamespace)).
		Named("workloadavailabilityconfig").
		Complete(r)
}
