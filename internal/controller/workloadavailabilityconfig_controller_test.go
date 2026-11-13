package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/components"
	"github.com/medik8s/workload-availability-operator/internal/images"
	"github.com/medik8s/workload-availability-operator/internal/operands"
)

const ns = "wao-test"

func componentSpec(w *v1alpha1.WorkloadAvailabilityConfig, key string) *v1alpha1.ComponentSpec {
	switch key {
	case v1alpha1.ComponentKeyNodeHealthCheck:
		return &w.Spec.Components.NodeHealthCheck
	case v1alpha1.ComponentKeySelfNodeRemediation:
		return &w.Spec.Components.SelfNodeRemediation
	case v1alpha1.ComponentKeyStorageBasedRemediation:
		return &w.Spec.Components.StorageBasedRemediation
	case v1alpha1.ComponentKeyFenceAgentsRemediation:
		return &w.Spec.Components.FenceAgentsRemediation
	case v1alpha1.ComponentKeyMachineDeletionRemediation:
		return &w.Spec.Components.MachineDeletionRemediation
	case v1alpha1.ComponentKeyNodeMaintenance:
		return &w.Spec.Components.NodeMaintenance
	default:
		panic("unknown component: " + key)
	}
}

func setResources(ctx context.Context, key string, resources *corev1.ResourceRequirements) {
	Eventually(func() error {
		w := getWAC(ctx)
		if resources == nil {
			componentSpec(w, key).Manager = nil
		} else {
			componentSpec(w, key).Manager = &v1alpha1.ManagerSpec{Resources: toManagerResources(resources)}
		}
		return k8sClient.Update(ctx, w)
	}).Should(Succeed())
}

// toManagerResources writes only CPU and memory, the fields the API exposes.
func toManagerResources(in *corev1.ResourceRequirements) *v1alpha1.ManagerResources {
	convert := func(list corev1.ResourceList) *v1alpha1.CPUMemory {
		if list == nil {
			return nil
		}
		out := &v1alpha1.CPUMemory{}
		if q, ok := list[corev1.ResourceCPU]; ok {
			out.CPU = ptr.To(v1alpha1.Quantity(q.String()))
		}
		if q, ok := list[corev1.ResourceMemory]; ok {
			out.Memory = ptr.To(v1alpha1.Quantity(q.String()))
		}
		return out
	}
	return &v1alpha1.ManagerResources{Requests: convert(in.Requests), Limits: convert(in.Limits)}
}

func getDeployment(ctx context.Context, name string) *appsv1.Deployment {
	d := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, d)).To(Succeed())
	return d
}

func managerResources(d *appsv1.Deployment) corev1.ResourceRequirements {
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "manager" {
			return c.Resources
		}
	}
	panic("manager container missing")
}

func getWAC(ctx context.Context) *v1alpha1.WorkloadAvailabilityConfig {
	w := &v1alpha1.WorkloadAvailabilityConfig{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: v1alpha1.SingletonName}, w)).To(Succeed())
	return w
}

func component(w *v1alpha1.WorkloadAvailabilityConfig, key string) *v1alpha1.ComponentStatus {
	for i := range w.Status.Components {
		if w.Status.Components[i].Name == key {
			return &w.Status.Components[i]
		}
	}
	return nil
}

func setEnabled(ctx context.Context, key string, enabled bool) {
	Eventually(func() error {
		w := getWAC(ctx)
		switch key {
		case v1alpha1.ComponentKeyNodeHealthCheck:
			w.Spec.Components.NodeHealthCheck.Enabled = enabled
		case v1alpha1.ComponentKeySelfNodeRemediation:
			w.Spec.Components.SelfNodeRemediation.Enabled = enabled
		case v1alpha1.ComponentKeyStorageBasedRemediation:
			w.Spec.Components.StorageBasedRemediation.Enabled = enabled
		case v1alpha1.ComponentKeyFenceAgentsRemediation:
			w.Spec.Components.FenceAgentsRemediation.Enabled = enabled
		case v1alpha1.ComponentKeyMachineDeletionRemediation:
			w.Spec.Components.MachineDeletionRemediation.Enabled = enabled
		case v1alpha1.ComponentKeyNodeMaintenance:
			w.Spec.Components.NodeMaintenance.Enabled = enabled
		}
		return k8sClient.Update(ctx, w)
	}).Should(Succeed())
}

// Names of the seeded FAR registrations. The central names follow the classic OLM pattern
// <generateName>-<suffix>. The legacy names are those of a standalone FAR installation.
var (
	farCentralRegistrations = []string{"mfenceagentsremediation.kb.io-waotest", "vfenceagentsremediation.kb.io-waotest"}
	farLegacyRegistrations  = []string{"fence-agents-remediation-mutating-webhook-configuration", "fence-agents-remediation-validating-webhook-configuration"}
)

// seedFARRegistrations creates registrations that WAO did not create: a central pair, as OLM
// installs with WAO, and a foreign legacy pair. An object selector that matches no object keeps
// them out of the envtest request path.
func seedFARRegistrations(ctx context.Context) {
	rules := []admissionregistrationv1.RuleWithOperations{{
		Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
		Rule: admissionregistrationv1.Rule{APIGroups: []string{"fence-agents-remediation.medik8s.io"}, APIVersions: []string{"v1alpha1"},
			Resources: []string{"fenceagentsremediations"}},
	}}
	clientConfig := admissionregistrationv1.WebhookClientConfig{Service: &admissionregistrationv1.ServiceReference{
		Namespace: ns, Name: "workload-availability-operator-controller-manager-service", Path: ptr.To("/wao-test")}}
	never := &metav1.LabelSelector{MatchLabels: map[string]string{"wao-test/never-matches": "true"}}
	for _, names := range [][]string{farCentralRegistrations, farLegacyRegistrations} {
		Expect(k8sClient.Create(ctx, &admissionregistrationv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: names[0]},
			Webhooks: []admissionregistrationv1.MutatingWebhook{{Name: "mfenceagentsremediation.kb.io", Rules: rules, ClientConfig: clientConfig,
				ObjectSelector: never, FailurePolicy: ptr.To(admissionregistrationv1.Fail),
				SideEffects: ptr.To(admissionregistrationv1.SideEffectClassNone), AdmissionReviewVersions: []string{"v1"}}},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: names[1]},
			Webhooks: []admissionregistrationv1.ValidatingWebhook{{Name: "vfenceagentsremediation.kb.io", Rules: rules, ClientConfig: clientConfig,
				ObjectSelector: never, FailurePolicy: ptr.To(admissionregistrationv1.Fail),
				SideEffects: ptr.To(admissionregistrationv1.SideEffectClassNone), AdmissionReviewVersions: []string{"v1"}}},
		})).To(Succeed())
	}
}

func farRegistrationObjects() []client.Object {
	objs := make([]client.Object, 0, 4)
	for _, names := range [][]string{farCentralRegistrations, farLegacyRegistrations} {
		objs = append(objs,
			&admissionregistrationv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: names[0]}},
			&admissionregistrationv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: names[1]}})
	}
	return objs
}

// farRegistrationVersions fails if a seeded registration is absent, has an owner, or has the WAO
// label. It returns the resource versions, so a caller can detect each write.
func farRegistrationVersions(ctx context.Context) map[string]string {
	versions := map[string]string{}
	for _, o := range farRegistrationObjects() {
		ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(o), o)).To(Succeed(), o.GetName())
		ExpectWithOffset(1, o.GetOwnerReferences()).To(BeEmpty(), o.GetName())
		ExpectWithOffset(1, o.GetLabels()).NotTo(HaveKey(operands.LabelManagedBy), o.GetName())
		ExpectWithOffset(1, o.GetDeletionTimestamp()).To(BeNil(), o.GetName())
		versions[fmt.Sprintf("%T/%s", o, o.GetName())] = o.GetResourceVersion()
	}
	return versions
}

func deleteFARRegistrations(ctx context.Context) {
	for _, o := range farRegistrationObjects() {
		Expect(k8sClient.Delete(ctx, o)).To(Succeed())
	}
}

var _ = Describe("WorkloadAvailabilityConfig controller", Ordered, func() {
	ctx := context.Background()

	It("rejects a non-singleton name via CEL", func() {
		bad := &v1alpha1.WorkloadAvailabilityConfig{ObjectMeta: metav1.ObjectMeta{Name: "other"}}
		err := k8sClient.Create(ctx, bad)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("singleton"))
	})

	It("reports Available with every component Disabled and creates nothing", func() {
		Expect(k8sClient.Create(ctx, &v1alpha1.WorkloadAvailabilityConfig{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.SingletonName}})).To(Succeed())
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(meta.IsStatusConditionTrue(w.Status.Conditions, v1alpha1.ConditionAvailable)).To(BeTrue())
			g.Expect(w.Status.Components).To(HaveLen(6))
			for _, c := range w.Status.Components {
				g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			}
			g.Expect(w.Status.InstallNamespace).To(Equal(ns))
			g.Expect(w.Status.ReleaseSet).To(HaveLen(len(images.Required)))
			g.Expect(w.Finalizers).To(ContainElement(Finalizer))
		}).Should(Succeed())
		deps := &appsv1.DeploymentList{}
		Expect(k8sClient.List(ctx, deps, client.InNamespace(ns))).To(Succeed())
		Expect(deps.Items).To(BeEmpty())
	})

	It("does not rewrite status when nothing changes", func() {
		rv := getWAC(ctx).ResourceVersion
		Consistently(func() string { return getWAC(ctx).ResourceVersion }, 5*time.Second, 250*time.Millisecond).Should(Equal(rv))
	})

	It("rejects strings for enabled and malformed resource quantities", func() {
		for _, value := range []string{"true", "false", "Enabled"} {
			patch := []byte(fmt.Sprintf(`{"spec":{"components":{"nodeHealthCheck":{"enabled":%q}}}}`, value))
			err := k8sClient.Patch(ctx, getWAC(ctx), client.RawPatch(types.MergePatchType, patch))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		}
		bad := []byte(`{"spec":{"components":{"nodeHealthCheck":{"manager":{"resources":{"requests":{"cpu":"not-a-quantity"}}}}}}}`)
		err := k8sClient.Patch(ctx, getWAC(ctx), client.RawPatch(types.MergePatchType, bad))
		Expect(err).To(HaveOccurred())
		legacy := []byte(`{"spec":{"components":{"nodeHealthCheck":{"state":"Enabled"}}}}`)
		err = k8sClient.Patch(ctx, getWAC(ctx), client.RawPatch(types.MergePatchType, legacy), client.FieldValidation(metav1.FieldValidationStrict))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("state"))
	})

	It("rejects malformed quantity exponents while the component is disabled", func() {
		for _, field := range []string{"requests", "limits"} {
			for _, value := range []string{"1e1.5", "1e.5", "1e9223372036854775808", "1e-9223372036854775809", "1e9223372036854775807", "1e2147483648", "1e100000000", "1e1000", "bogus"} {
				for _, name := range []string{"cpu", "memory"} {
					patch := []byte(fmt.Sprintf(`{"spec":{"components":{"nodeHealthCheck":{"enabled":false,"manager":{"resources":{%q:{%q:%q}}}}}}}`, field, name, value))
					target := &unstructured.Unstructured{Object: map[string]interface{}{
						"apiVersion": v1alpha1.GroupVersion.String(), "kind": "WorkloadAvailabilityConfig",
						"metadata": map[string]interface{}{"name": v1alpha1.SingletonName},
					}}
					err := k8sClient.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.DryRunAll)
					Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s.%s=%s should fail admission: %v", field, name, value, err)
				}
			}
		}
	})

	It("accepts integer and string resource quantities through the console schema", func() {
		for _, field := range []string{"requests", "limits"} {
			for _, name := range []string{"cpu", "memory"} {
				for _, value := range []string{`1`, `0`, `"100m"`, `"128Mi"`, `"1e3"`, `"1e-999"`} {
					for _, patchType := range []types.PatchType{types.MergePatchType, types.ApplyPatchType} {
						patch := []byte(fmt.Sprintf(`{"apiVersion":"workloadavailability.medik8s.io/v1alpha1","kind":"WorkloadAvailabilityConfig","metadata":{"name":"cluster"},"spec":{"components":{"nodeHealthCheck":{"enabled":false,"manager":{"resources":{%q:{%q:%s}}}}}}}`, field, name, value))
						target := &unstructured.Unstructured{Object: map[string]interface{}{
							"apiVersion": v1alpha1.GroupVersion.String(), "kind": "WorkloadAvailabilityConfig",
							"metadata": map[string]interface{}{"name": v1alpha1.SingletonName},
						}}
						Expect(k8sClient.Patch(ctx, target, client.RawPatch(patchType, patch), client.DryRunAll, client.FieldOwner("wao-resource-admission-test"))).To(Succeed(), "%s %s.%s=%s", patchType, field, name, value)
						stored, found, err := unstructured.NestedFieldNoCopy(target.Object, "spec", "components", "nodeHealthCheck", "manager", "resources", field, name)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(fmt.Sprint(stored)).To(Equal(strings.Trim(value, `"`)))
					}
				}
			}
		}
	})

	It("rejects unsupported resource names with strict field validation", func() {
		for _, field := range []string{"requests", "limits"} {
			for _, name := range []string{"storage", "ephemeral-storage", "unsupported"} {
				patch := []byte(fmt.Sprintf(`{"spec":{"components":{"nodeHealthCheck":{"manager":{"resources":{%q:{%q:"1Gi"}}}}}}}`, field, name))
				err := k8sClient.Patch(ctx, getWAC(ctx), client.RawPatch(types.MergePatchType, patch), client.DryRunAll, client.FieldValidation(metav1.FieldValidationStrict))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(name))
			}
		}
	})

	It("stores resources while disabled and applies them to every named manager", func() {
		resources := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		}
		for _, def := range components.All() {
			setResources(ctx, def.Key, resources)
		}
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			deps := &appsv1.DeploymentList{}
			g.Expect(k8sClient.List(ctx, deps, client.InNamespace(ns))).To(Succeed())
			g.Expect(deps.Items).To(BeEmpty())
		}).Should(Succeed())
		for _, def := range components.All() {
			setEnabled(ctx, def.Key, true)
			Eventually(func(g Gomega) {
				d := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: def.ManagerDeployment}, d)).To(Succeed())
				got := managerResources(d)
				for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
					request, limit := got.Requests[name], got.Limits[name]
					g.Expect(request.Cmp(resources.Requests[name])).To(BeZero())
					g.Expect(limit.Cmp(resources.Limits[name])).To(BeZero())
				}
			}).Should(Succeed())
		}
		for _, def := range components.All() {
			setEnabled(ctx, def.Key, false)
			setResources(ctx, def.Key, nil)
		}
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			g.Expect(w.Status.Components).To(HaveLen(6))
			for _, c := range w.Status.Components {
				g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled), c.Name)
			}
			deps := &appsv1.DeploymentList{}
			g.Expect(k8sClient.List(ctx, deps, client.InNamespace(ns))).To(Succeed())
			g.Expect(deps.Items).To(BeEmpty())
		}).Should(Succeed())
	})

	It("installs the NHC operand when enabled", func() {
		setEnabled(ctx, v1alpha1.ComponentKeyNodeHealthCheck, true)
		// Wait for status from this enable generation, not a previous installation.
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, v1alpha1.ComponentKeyNodeHealthCheck)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseProgressing)) // envtest has no kubelet or service-ca
			g.Expect(c.Deployments).To(HaveLen(2))                          // the manager and the console plugin backend of NHC's OCP packaging
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(false)))             // the listener callback of this suite reports false here
		}).Should(Succeed())
		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "node-healthcheck-controller-manager"}, dep)
		}).Should(Succeed())
		Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImages().Get(images.NHCManager)))
		Expect(dep.Labels[operands.LabelManagedBy]).To(Equal(operands.ManagedByValue))
		Expect(dep.OwnerReferences).To(HaveLen(1))
		Expect(dep.OwnerReferences[0].Kind).To(Equal("WorkloadAvailabilityConfig"))
		Expect(*dep.OwnerReferences[0].Controller).To(BeTrue())
		console := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "node-healthcheck-node-remediation-console-plugin"}, console)
		}).Should(Succeed())
		Expect(console.Spec.Template.Spec.Containers[0].Image).To(Equal(testImages().Get(images.NHCConsole)))
		Expect(console.OwnerReferences).To(HaveLen(1))

		// Operand RBAC comes from the WAO CSV (installed by OLM), never from the operand renderer.
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "node-healthcheck-controller-manager"}, &rbacv1.ClusterRole{}))).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "node-healthcheck-controller-manager"}, &rbacv1.Role{}))).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "node-healthcheck-controller-manager"}, &corev1.ServiceAccount{}))).To(BeTrue())
		// OLM installs the NHC registration with WAO. The NHC operand renders no admission configuration.
		vw := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "node-healthcheck-validating-webhook-configuration"}, vw))).To(BeTrue())
	})

	It("updates resources, repairs drift, restores defaults, and isolates other Deployments", func() {
		const name = "node-healthcheck-controller-manager"
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, true)
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "sbr-operator-controller-manager"}, &appsv1.Deployment{})
		}).Should(Succeed())
		other := getDeployment(ctx, "sbr-operator-controller-manager").Spec.Template.DeepCopy()
		plugin := getDeployment(ctx, "node-healthcheck-node-remediation-console-plugin").Spec.Template.DeepCopy()
		defaults := managerResources(getDeployment(ctx, name))
		resources := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		}
		setResources(ctx, v1alpha1.ComponentKeyNodeHealthCheck, resources)
		Eventually(func(g Gomega) {
			got := managerResources(getDeployment(ctx, name))
			g.Expect(got.Requests.Memory().Cmp(resource.MustParse("64Mi"))).To(BeZero())
			g.Expect(got.Limits.Memory().Cmp(resource.MustParse("256Mi"))).To(BeZero())
			g.Expect(got.Requests.Cpu().Cmp(*defaults.Requests.Cpu())).To(BeZero())
		}).Should(Succeed())
		before := getDeployment(ctx, name).Generation
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("96Mi")
		setResources(ctx, v1alpha1.ComponentKeyNodeHealthCheck, resources)
		Eventually(func(g Gomega) {
			d := getDeployment(ctx, name)
			got := managerResources(d)
			g.Expect(got.Requests.Memory().Cmp(resource.MustParse("96Mi"))).To(BeZero())
			g.Expect(d.Generation).To(BeNumerically(">", before))
		}).Should(Succeed())
		Eventually(func() error {
			d := getDeployment(ctx, name)
			original := d.DeepCopy()
			for i := range d.Spec.Template.Spec.Containers {
				if d.Spec.Template.Spec.Containers[i].Name == "manager" {
					d.Spec.Template.Spec.Containers[i].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("80Mi")
				}
			}
			return k8sClient.Patch(ctx, d, client.MergeFrom(original))
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			got := managerResources(getDeployment(ctx, name))
			g.Expect(got.Requests.Memory().Cmp(resource.MustParse("96Mi"))).To(BeZero())
		}).Should(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyNodeHealthCheck, nil)
		Eventually(func(g Gomega) {
			g.Expect(managerResources(getDeployment(ctx, name))).To(Equal(defaults))
		}).Should(Succeed())
		Expect(getDeployment(ctx, "sbr-operator-controller-manager").Spec.Template).To(Equal(*other))
		Expect(getDeployment(ctx, "node-healthcheck-node-remediation-console-plugin").Spec.Template).To(Equal(*plugin))
		generation := getDeployment(ctx, name).Generation
		Consistently(func() int64 { return getDeployment(ctx, name).Generation }, 2*time.Second, 100*time.Millisecond).Should(Equal(generation))
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, false)
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "sbr-operator-controller-manager"}, &appsv1.Deployment{}))
		}).Should(BeTrue())
	})

	It("reports invalid effective resources without applying them and recovers after correction", func() {
		const name = "sbr-operator-controller-manager"
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, true)
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{})
		}).Should(Succeed())
		before := getDeployment(ctx, name).Spec.Template.DeepCopy()
		bad := &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, bad)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			c := component(w, v1alpha1.ComponentKeyStorageBasedRemediation)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDegraded))
			g.Expect(c.Message).To(ContainSubstring("spec.components.storageBasedRemediation.manager.resources.requests.memory"))
			g.Expect(c.Message).To(ContainSubstring("64Mi"))
			g.Expect(c.Message).To(ContainSubstring("32Mi"))
			g.Expect(meta.IsStatusConditionTrue(w.Status.Conditions, v1alpha1.ConditionDegraded)).To(BeTrue())
		}).Should(Succeed())
		Expect(getDeployment(ctx, name).Spec.Template).To(Equal(*before))
		setResources(ctx, v1alpha1.ComponentKeyNodeHealthCheck, &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("48Mi")},
		})
		Eventually(func(g Gomega) {
			got := managerResources(getDeployment(ctx, "node-healthcheck-controller-manager"))
			g.Expect(got.Requests.Memory().Cmp(resource.MustParse("48Mi"))).To(BeZero())
		}).Should(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, nil)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(component(w, v1alpha1.ComponentKeyStorageBasedRemediation).Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
			g.Expect(meta.IsStatusConditionTrue(w.Status.Conditions, v1alpha1.ConditionDegraded)).To(BeFalse())
		}).Should(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, bad)
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, false)
		Eventually(func(g Gomega) {
			g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeyStorageBasedRemediation).Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{}))).To(BeTrue())
		}).Should(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, nil)
		setResources(ctx, v1alpha1.ComponentKeyNodeHealthCheck, nil)
	})

	It("refuses to adopt a foreign object with the same name", func() {
		foreign := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sbr-operator-controller-manager", Namespace: ns},
			Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foreign"}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foreign"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "example.test/foreign"}}}}}}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")}})
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, true)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			c := component(w, v1alpha1.ComponentKeyStorageBasedRemediation)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDegraded))
			g.Expect(c.Message).To(ContainSubstring("not managed by"))
			g.Expect(meta.IsStatusConditionTrue(w.Status.Conditions, v1alpha1.ConditionDegraded)).To(BeTrue())
		}).Should(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foreign), foreign)).To(Succeed())
		Expect(foreign.Spec.Template.Spec.Containers[0].Image).To(Equal("example.test/foreign"))
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, false)
		Eventually(func(g Gomega) {
			c := component(getWAC(ctx), v1alpha1.ComponentKeyStorageBasedRemediation)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			g.Expect(c.Message).To(ContainSubstring("foreign"))
		}).Should(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foreign), foreign)).To(Succeed(), "WAO must not delete a foreign object on disable")
		Expect(k8sClient.Delete(ctx, foreign)).To(Succeed()) // test cleanup, not WAO behavior
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, nil)
	})

	It("removes the NHC operand when disabled and reports central admission", func() {
		setEnabled(ctx, v1alpha1.ComponentKeyNodeHealthCheck, false)
		// Status is written after every operand object has been deleted, so wait for it first.
		Eventually(func(g Gomega) {
			c := component(getWAC(ctx), v1alpha1.ComponentKeyNodeHealthCheck)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			// WAO serves NHC admission also in the Disabled state. The listener callback of this suite reports false here.
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(false)))
			g.Expect(c.Message).NotTo(ContainSubstring("unavailable while Disabled"))
		}).Should(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "node-healthcheck-controller-manager"}, &appsv1.Deployment{}))).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "node-healthcheck-validating-webhook-configuration"}, &admissionregistrationv1.ValidatingWebhookConfiguration{}))).To(BeTrue())
	})

	It("creates no RBAC for any component and removes every runtime object on disable", func() {
		for _, key := range v1alpha1.AllComponentKeys {
			setEnabled(ctx, key, true)
		}
		Eventually(func(g Gomega) {
			for _, key := range v1alpha1.AllComponentKeys {
				c := component(getWAC(ctx), key)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
			}
		}).Should(Succeed())
		for _, def := range components.All() {
			for _, name := range def.Deployments {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{})).To(Succeed())
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &corev1.ServiceAccount{}))).To(BeTrue(), name)
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &rbacv1.ClusterRole{}))).To(BeTrue(), name)
			}
		}
		for _, key := range v1alpha1.AllComponentKeys {
			setEnabled(ctx, key, false)
		}
		Eventually(func(g Gomega) {
			for _, def := range components.All() {
				for _, name := range def.Deployments {
					g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{}))).To(BeTrue())
				}
			}
		}).Should(Succeed())
		// Status is written after the deletes; wait for it so the next spec does not see a stale Progressing phase.
		Eventually(func(g Gomega) {
			for _, key := range v1alpha1.AllComponentKeys {
				g.Expect(component(getWAC(ctx), key).Phase).To(Equal(v1alpha1.ComponentPhaseDisabled), key)
			}
		}).Should(Succeed())
	})

	It("reports DisableIncomplete when SNR agents survive a manager-level disable", func() {
		// Simulate what the SNR manager creates at runtime. Do it while SNR is Disabled: once enabled,
		// SNR's Fail-policy webhooks are registered and envtest has no backend to answer them.
		cfg := &unstructured.Unstructured{}
		cfg.SetGroupVersionKind(schema.GroupVersionKind{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediationConfig"})
		cfg.SetName("self-node-remediation-config")
		cfg.SetNamespace(ns)
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "self-node-remediation-ds", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "self-node-remediation.medik8s.io/v1alpha1", Kind: "SelfNodeRemediationConfig", Name: cfg.GetName(), UID: cfg.GetUID(), Controller: ptr.To(true)}}},
			Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "snr-agent"}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "snr-agent"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "example.test/agent"}}}}}}
		Expect(k8sClient.Create(ctx, ds)).To(Succeed())

		setEnabled(ctx, v1alpha1.ComponentKeySelfNodeRemediation, true)
		Eventually(func(g Gomega) {
			g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeySelfNodeRemediation).Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
		}).Should(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "self-node-remediation-controller-manager"}, &appsv1.Deployment{})).To(Succeed())

		setEnabled(ctx, v1alpha1.ComponentKeySelfNodeRemediation, false)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			c := component(w, v1alpha1.ComponentKeySelfNodeRemediation)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisableIncomplete))
			g.Expect(strings.Join(c.RetainedResources, "\n")).To(ContainSubstring("DaemonSet " + ns + "/self-node-remediation-ds"))
			g.Expect(strings.Join(c.RetainedResources, "\n")).To(ContainSubstring("SelfNodeRemediationConfig " + ns + "/self-node-remediation-config"))
			g.Expect(meta.FindStatusCondition(w.Status.Conditions, v1alpha1.ConditionAvailable).Reason).To(Equal("DisableIncomplete"))
		}).Should(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "self-node-remediation-controller-manager"}, &appsv1.Deployment{}))).To(BeTrue())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(Succeed(), "WAO must not delete the agent DaemonSet")
		Expect(k8sClient.Delete(ctx, ds)).To(Succeed())
		Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
	})

	It("installs FAR, MDR and NMO with component-isolated selectors", func() {
		for _, key := range []string{v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation, v1alpha1.ComponentKeyNodeMaintenance} {
			setEnabled(ctx, key, true)
		}
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			for _, key := range []string{v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation, v1alpha1.ComponentKeyNodeMaintenance} {
				g.Expect(component(w, key).Phase).To(Equal(v1alpha1.ComponentPhaseProgressing), key)
			}
		}).Should(Succeed())
		for _, name := range []string{"fence-agents-remediation-controller-manager", "machine-deletion-remediation-controller-manager", "node-maintenance-operator-controller-manager"} {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{})).To(Succeed(), name)
		}
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "machine-deletion-remediation-controller-manager-metrics-service"}, svc)).To(Succeed())
		Expect(svc.Spec.Selector).To(HaveKeyWithValue("workloadavailability.medik8s.io/component", v1alpha1.ComponentKeyMachineDeletionRemediation))
		// OLM installs the FAR and NMO registrations with WAO. These operands render no admission configuration.
		for _, name := range []string{"fence-agents-remediation-mutating-webhook-configuration", "fence-agents-remediation-validating-webhook-configuration",
			"node-maintenance-operator-validating-webhook-configuration"} {
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &admissionregistrationv1.MutatingWebhookConfiguration{}))).To(BeTrue(), name)
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &admissionregistrationv1.ValidatingWebhookConfiguration{}))).To(BeTrue(), name)
		}
		for _, key := range []string{v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation, v1alpha1.ComponentKeyNodeMaintenance} {
			setEnabled(ctx, key, false)
		}
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			for _, key := range []string{v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation, v1alpha1.ComponentKeyNodeMaintenance} {
				g.Expect(component(w, key).Phase).To(Equal(v1alpha1.ComponentPhaseDisabled), key)
			}
		}).Should(Succeed())
	})

	It("reports a NodeMaintenance left behind by a disabled NMO as retained", func() {
		// This suite has no NMO registration: on a cluster, OLM installs it with WAO and the WAO manager
		// validates the create in each component state.
		nm := &unstructured.Unstructured{}
		nm.SetGroupVersionKind(schema.GroupVersionKind{Group: "nodemaintenance.medik8s.io", Version: "v1beta1", Kind: "NodeMaintenance"})
		nm.SetName("wao-test-maintenance")
		Expect(unstructured.SetNestedField(nm.Object, "wao-test-node", "spec", "nodeName")).To(Succeed())
		Expect(unstructured.SetNestedField(nm.Object, "envtest", "spec", "reason")).To(Succeed())
		Expect(k8sClient.Create(ctx, nm)).To(Succeed())
		setEnabled(ctx, v1alpha1.ComponentKeyNodeMaintenance, true)
		Eventually(func(g Gomega) {
			g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeyNodeMaintenance).Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
		}).Should(Succeed())
		setEnabled(ctx, v1alpha1.ComponentKeyNodeMaintenance, false)
		Eventually(func(g Gomega) {
			c := component(getWAC(ctx), v1alpha1.ComponentKeyNodeMaintenance)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisableIncomplete))
			g.Expect(c.RetainedResources).To(ContainElement("nodemaintenance.medik8s.io/v1beta1 NodeMaintenance wao-test-maintenance"))
		}).Should(Succeed())
		Expect(k8sClient.Delete(ctx, nm)).To(Succeed())
		Eventually(func(g Gomega) { // picked up by the periodic re-check
			g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeyNodeMaintenance).Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
		}, 90*time.Second, time.Second).Should(Succeed())
	})

	It("reports the NodeHealthChecks that admission blocks while NHC is Disabled", func() {
		nhcKey, mdr := v1alpha1.ComponentKeyNodeHealthCheck, v1alpha1.ComponentKeyMachineDeletionRemediation
		gvk := schema.GroupVersionKind{Group: "remediation.medik8s.io", Version: "v1alpha1", Kind: "NodeHealthCheck"}
		// This suite has no NHC registration and no NHC manager, so these objects start no remediation.
		newPolicy := func(name string) *unstructured.Unstructured {
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(gvk)
			policy.SetName(name)
			Expect(unstructured.SetNestedField(policy.Object, "51%", "spec", "minHealthy")).To(Succeed())
			Expect(unstructured.SetNestedStringMap(policy.Object, map[string]string{"wao.test/none": "true"}, "spec", "selector", "matchLabels")).To(Succeed())
			Expect(unstructured.SetNestedStringMap(policy.Object, map[string]string{"apiVersion": "wao.test/v1",
				"kind": "SyntheticRemediationTemplate", "namespace": ns, "name": "template"}, "spec", "remediationTemplate")).To(Succeed())
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			return policy
		}
		setRemediations := func(policy *unstructured.Unstructured, remediations []any) {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
			nodes := []any{map[string]any{"name": "wao-test-node", "remediations": remediations}}
			Expect(unstructured.SetNestedSlice(policy.Object, nodes, "status", "unhealthyNodes")).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())
		}
		blocked, idle := newPolicy("wao-blocked-policy"), newPolicy("wao-idle-policy")
		setRemediations(blocked, []any{map[string]any{"started": "2026-11-13T00:00:00Z", "resource": map[string]any{
			"apiVersion": "wao.test/v1", "kind": "SyntheticRemediation", "namespace": ns, "name": "wao-test-node"}}})
		setRemediations(idle, []any{})

		setEnabled(ctx, mdr, true) // any spec change starts a reconcile of each component
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, nhcKey)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisableIncomplete))
			g.Expect(c.RetainedResources).To(ConsistOf(
				"remediation.medik8s.io/v1alpha1 NodeHealthCheck wao-blocked-policy",
				"remediation.medik8s.io/v1alpha1 NodeHealthCheck wao-idle-policy"))
			g.Expect(c.Message).To(ContainSubstring("[NodeHealthCheck wao-blocked-policy]: the status shows a remediation"))
			g.Expect(c.Message).To(ContainSubstring("admission rejects the delete"))
			g.Expect(c.Message).To(ContainSubstring("set the component to Enabled again"))
			g.Expect(c.Message).To(ContainSubstring("patch to the status subresource"))
		}).Should(Succeed())

		// The status patch is the documented procedure. Then the report has no blocked object.
		setRemediations(blocked, []any{})
		setEnabled(ctx, mdr, false)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, nhcKey)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisableIncomplete))
			g.Expect(c.RetainedResources).To(HaveLen(2))
			g.Expect(c.Message).NotTo(ContainSubstring("admission rejects"))
		}).Should(Succeed())

		// With no policy, the component is Disabled with no message.
		Expect(k8sClient.Delete(ctx, blocked)).To(Succeed())
		Expect(k8sClient.Delete(ctx, idle)).To(Succeed())
		setEnabled(ctx, mdr, true)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, nhcKey)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			g.Expect(c.Message).To(BeEmpty())
		}).Should(Succeed())
		setEnabled(ctx, mdr, false)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			g.Expect(component(w, mdr).Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
		}).Should(Succeed())
	})

	It("reports central FAR admission in each state and keeps central and foreign registrations", func() {
		far, mdr := v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation
		seedFARRegistrations(ctx)
		before := farRegistrationVersions(ctx)

		// Disabled FAR with a serving listener: admission is ready and there is no warning.
		listenerReady.Store(true)
		setEnabled(ctx, mdr, true) // any spec change starts a reconcile of each component
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, far)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(true)))
			g.Expect(c.Message).NotTo(ContainSubstring("admission"))
		}).Should(Succeed())

		// Enabled FAR reports the same callback.
		setEnabled(ctx, far, true)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, far)
			g.Expect(c.DesiredState).To(Equal(v1alpha1.ComponentStateEnabled))
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(true)))
		}).Should(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "fence-agents-remediation-controller-manager"}, &appsv1.Deployment{})).To(Succeed())

		// A listener that does not serve gives false in the two states.
		listenerReady.Store(false)
		setEnabled(ctx, mdr, false)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, far)
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(false)))
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseProgressing))
		}).Should(Succeed())
		setEnabled(ctx, far, false)
		Eventually(func(g Gomega) {
			w := getWAC(ctx)
			g.Expect(w.Status.ObservedGeneration).To(Equal(w.Generation))
			c := component(w, far)
			g.Expect(c.Phase).To(Equal(v1alpha1.ComponentPhaseDisabled))
			g.Expect(c.AdmissionReady).To(Equal(ptr.To(false)))
			g.Expect(c.Message).NotTo(ContainSubstring("admission"))
		}).Should(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "fence-agents-remediation-controller-manager"}, &appsv1.Deployment{}))).To(BeTrue())

		// FAR enable and disable did not change, adopt, or delete a registration.
		Expect(farRegistrationVersions(ctx)).To(Equal(before))
	})

	It("runs the disable sequence and drops its finalizer on deletion", func() {
		registrationsBefore := farRegistrationVersions(ctx)
		DeferCleanup(func() {
			// WAC deletion and finalization keep the central and the foreign registrations.
			Expect(farRegistrationVersions(ctx)).To(Equal(registrationsBefore))
			deleteFARRegistrations(ctx)
		})
		setEnabled(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, true)
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "sbr-operator-controller-manager"}, &appsv1.Deployment{})
		}).Should(Succeed())
		setResources(ctx, v1alpha1.ComponentKeyStorageBasedRemediation, &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}})
		Eventually(func(g Gomega) {
			g.Expect(component(getWAC(ctx), v1alpha1.ComponentKeyStorageBasedRemediation).Phase).To(Equal(v1alpha1.ComponentPhaseDegraded))
		}).Should(Succeed())
		Expect(k8sClient.Delete(ctx, getWAC(ctx))).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: v1alpha1.SingletonName}, &v1alpha1.WorkloadAvailabilityConfig{}))
		}).Should(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "sbr-operator-controller-manager"}, &appsv1.Deployment{}))).To(BeTrue(), "explicit deletion; envtest has no garbage collector")
	})
})
