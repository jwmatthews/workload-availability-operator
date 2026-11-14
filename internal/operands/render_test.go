package operands_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/components"
	"github.com/medik8s/workload-availability-operator/internal/images"
	"github.com/medik8s/workload-availability-operator/internal/operands"
)

func TestRenderedManagerResourcesForEveryComponent(t *testing.T) {
	for _, def := range components.All() {
		t.Run(def.Key, func(t *testing.T) {
			for _, overrides := range []*corev1.ResourceRequirements{nil, {}, {
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
			}} {
				objects, err := operands.Render(def, testNamespace, testImages())
				if err != nil {
					t.Fatal(err)
				}
				expected := make([]*unstructured.Unstructured, len(objects))
				for i, obj := range objects {
					expected[i] = obj.DeepCopy()
				}
				if overrides != nil && len(overrides.Requests) > 0 {
					dep := find(t, expected, "Deployment", def.ManagerDeployment)
					containers, _, err := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
					if err != nil {
						t.Fatal(err)
					}
					for _, raw := range containers {
						container := raw.(map[string]any)
						if container["name"] == def.ManagerContainer {
							container["resources"] = map[string]any{
								"requests": map[string]any{"cpu": "200m", "memory": "128Mi"},
								"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
							}
						}
					}
					if err := unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers"); err != nil {
						t.Fatal(err)
					}
				}
				if err := operands.ApplyManagerResources(def, objects, overrides); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(objects, expected) {
					t.Fatal("resource override changed fields outside the selected manager resources")
				}
			}
		})
	}
}

func TestRenderRestoresPackagedResourcesAfterOverrides(t *testing.T) {
	for _, def := range components.All() {
		t.Run(def.Key, func(t *testing.T) {
			first, err := operands.Render(def, testNamespace, testImages())
			if err != nil {
				t.Fatal(err)
			}
			overrides := &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			}
			if err := operands.ApplyManagerResources(def, first, overrides); err != nil {
				t.Fatal(err)
			}
			second, err := operands.Render(def, testNamespace, testImages())
			if err != nil {
				t.Fatal(err)
			}
			if err := operands.ApplyManagerResources(def, second, &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2m")},
			}); err != nil {
				t.Fatal(err)
			}
			third, err := operands.Render(def, testNamespace, testImages())
			if err != nil {
				t.Fatal(err)
			}
			managerResources := func(objects []*unstructured.Unstructured) map[string]any {
				dep := find(t, objects, "Deployment", def.ManagerDeployment)
				containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
				for _, raw := range containers {
					container := raw.(map[string]any)
					if container["name"] == def.ManagerContainer {
						resources, _, _ := unstructured.NestedMap(container, "resources")
						return resources
					}
				}
				t.Fatal("manager container is missing")
				return nil
			}
			secondResources, packagedResources := managerResources(second), managerResources(third)
			if !reflect.DeepEqual(secondResources["limits"], packagedResources["limits"]) {
				t.Fatal("a previous render changed the next render's packaged limits")
			}
			if got := secondResources["requests"].(map[string]any)["cpu"]; got != "2m" {
				t.Fatalf("second CPU request = %v, want 2m", got)
			}
			if got := managerResources(first)["requests"].(map[string]any)["cpu"]; got != "1m" {
				t.Fatalf("second render changed the first CPU request to %v", got)
			}
		})
	}
}

const testNamespace = "wao-ns"

func testImages() images.Set {
	s := images.Set{}
	for _, k := range images.Required {
		s[k] = "example.test/" + strings.ToLower(k) + "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	return s
}

func find(t *testing.T, objs []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for _, o := range objs {
		if o.GetKind() == kind && o.GetName() == name {
			return o
		}
	}
	t.Fatalf("%s/%s not rendered", kind, name)
	return nil
}

func TestRenderNHCReplacesNamespaceImagesAndLabels(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	objs, err := operands.Render(def, testNamespace, testImages())
	if err != nil {
		t.Fatal(err)
	}
	dep := find(t, objs, "Deployment", "node-healthcheck-controller-manager")
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	image := containers[0].(map[string]interface{})["image"].(string)
	if image != testImages().Get(images.NHCManager) {
		t.Fatalf("manager image = %q", image)
	}
	if strings.Contains(image, images.Placeholder(def.Images[0])) {
		t.Fatalf("tagged image was not replaced: %q", image)
	}
	for _, o := range objs {
		if o.GetLabels()[operands.LabelManagedBy] != operands.ManagedByValue || o.GetLabels()[operands.LabelComponent] != def.Key {
			t.Errorf("%s/%s missing WAO labels: %v", o.GetKind(), o.GetName(), o.GetLabels())
		}
		if operands.IsNamespaced(o.GetKind()) && o.GetNamespace() != testNamespace {
			t.Errorf("%s/%s namespace = %q", o.GetKind(), o.GetName(), o.GetNamespace())
		}
	}
	// OLM installs the NHC registration with WAO. The unchanged NHC image still starts its webhook
	// listener, so the Service of that listener stays.
	for _, o := range objs {
		if o.GetKind() == "ValidatingWebhookConfiguration" || o.GetKind() == "MutatingWebhookConfiguration" {
			t.Errorf("NHC renders the admission configuration %s", o.GetName())
		}
	}
	find(t, objs, "Service", "node-healthcheck-webhook-service")
}

// The WAO manager serves SNR admission, so the SNR operand renders no admission configuration.
// No operand renders one now; the renderer still puts such a kind after the Deployments.
func TestRenderOrdersServicesBeforeDeployments(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeySelfNodeRemediation)
	objs, err := operands.Render(def, testNamespace, testImages())
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, o := range objs {
		if _, seen := pos[o.GetKind()]; !seen {
			pos[o.GetKind()] = i
		}
	}
	service, hasService := pos["Service"]
	deployment, hasDeployment := pos["Deployment"]
	if !hasService || !hasDeployment || service >= deployment {
		t.Fatalf("unexpected order: %v", pos)
	}
	for _, kind := range []string{"ValidatingWebhookConfiguration", "MutatingWebhookConfiguration"} {
		if _, rendered := pos[kind]; rendered {
			t.Errorf("SNR renders a %s; OLM installs the SNR registrations with WAO", kind)
		}
	}
}

// RBAC is created by OLM from the WAO CSV (config/operands/<k>/overlays/rbac); the renderer must never emit it.
func TestRenderEmitsNoRBAC(t *testing.T) {
	for _, def := range components.All() {
		objs, err := operands.Render(def, testNamespace, testImages())
		if err != nil {
			t.Fatal(err)
		}
		if len(objs) == 0 {
			t.Fatalf("%s: nothing rendered", def.Key)
		}
		for _, o := range objs {
			switch o.GetKind() {
			case "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
				t.Errorf("%s: rendered %s/%s; RBAC belongs to the CSV", def.Key, o.GetKind(), o.GetName())
			}
		}
	}
}

// Design brief D10: all six managers share a namespace, so every pod template and every Service
// selector carries the component label; otherwise MDR's metrics Service would select every manager.
func TestRenderLabelsPodTemplatesAndServiceSelectors(t *testing.T) {
	for _, def := range components.All() {
		objs, err := operands.Render(def, testNamespace, testImages())
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			switch o.GetKind() {
			case "Deployment":
				labels, _, _ := unstructured.NestedStringMap(o.Object, "spec", "template", "metadata", "labels")
				if labels[operands.LabelComponent] != def.Key {
					t.Errorf("%s: Deployment/%s pod template label = %q", def.Key, o.GetName(), labels[operands.LabelComponent])
				}
			case "Service":
				sel, _, _ := unstructured.NestedStringMap(o.Object, "spec", "selector")
				if sel[operands.LabelComponent] != def.Key {
					t.Errorf("%s: Service/%s selector = %v", def.Key, o.GetName(), sel)
				}
			}
		}
	}
}

// Webhook operands mount exactly one service-ca certificate volume at the controller-runtime path.
func TestRenderWebhookOperandsMountOneCertVolume(t *testing.T) {
	for _, def := range components.All() {
		if len(def.Webhooks) == 0 {
			continue
		}
		objs, err := operands.Render(def, testNamespace, testImages())
		if err != nil {
			t.Fatal(err)
		}
		dep := find(t, objs, "Deployment", def.Deployments[0])
		containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		mounts := 0
		for _, c := range containers {
			vms, _, _ := unstructured.NestedSlice(c.(map[string]interface{}), "volumeMounts")
			for _, vm := range vms {
				if vm.(map[string]interface{})["mountPath"] == "/tmp/k8s-webhook-server/serving-certs" {
					mounts++
				}
			}
		}
		if mounts != 1 {
			t.Errorf("%s: %d mounts at the serving-certs path, want 1", def.Key, mounts)
		}
		vols, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
		certVols := 0
		for _, v := range vols {
			secret, _, _ := unstructured.NestedString(v.(map[string]interface{}), "secret", "secretName")
			if strings.HasSuffix(secret, "-webhook-server-cert") {
				certVols++
			}
		}
		if certVols != 1 {
			t.Errorf("%s: %d cert volumes, want 1 (volumes: %v)", def.Key, certVols, vols)
		}
	}
}

func TestRenderMDRManagerDefaults(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		overrides                  *corev1.ResourceRequirements
		requestMemory, limitMemory string
	}{
		{name: "packaged defaults", requestMemory: "64Mi", limitMemory: "128Mi"},
		{name: "empty overrides", overrides: &corev1.ResourceRequirements{}, requestMemory: "64Mi", limitMemory: "128Mi"},
		{
			name: "partial memory override",
			overrides: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			requestMemory: "64Mi", limitMemory: "256Mi",
		},
		{
			name: "explicit old values remain valid",
			overrides: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("20Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("30Mi")},
			},
			requestMemory: "20Mi", limitMemory: "30Mi",
		},
		{name: "fresh render restores defaults", requestMemory: "64Mi", limitMemory: "128Mi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := components.MDR()
			objects, err := operands.Render(def, testNamespace, testImages())
			if err != nil {
				t.Fatal(err)
			}
			if err := operands.ApplyManagerResources(def, objects, tc.overrides); err != nil {
				t.Fatal(err)
			}
			deployment := find(t, objects, "Deployment", def.ManagerDeployment)
			containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
			if err != nil || !found {
				t.Fatalf("containers missing: %v", err)
			}
			for _, raw := range containers {
				container := raw.(map[string]any)
				if container["name"] != def.ManagerContainer {
					continue
				}
				got, found, err := unstructured.NestedMap(container, "resources")
				if err != nil || !found {
					t.Fatalf("manager resources missing: %v", err)
				}
				want := map[string]any{
					"requests": map[string]any{"cpu": "100m", "memory": tc.requestMemory},
					"limits":   map[string]any{"cpu": "100m", "memory": tc.limitMemory},
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("manager resources = %v, want %v", got, want)
				}
				return
			}
			t.Fatal("manager container missing")
		})
	}
}

func TestRenderSNRReplacesAgentImageEnv(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeySelfNodeRemediation)
	objs, err := operands.Render(def, testNamespace, testImages())
	if err != nil {
		t.Fatal(err)
	}
	dep := find(t, objs, "Deployment", "self-node-remediation-controller-manager")
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	env, _, _ := unstructured.NestedSlice(containers[0].(map[string]interface{}), "env")
	found := false
	for _, e := range env {
		m := e.(map[string]interface{})
		if m["name"] == "SELF_NODE_REMEDIATION_IMAGE" {
			found = true
			if m["value"] != testImages().Get(images.SNRManager) {
				t.Fatalf("SELF_NODE_REMEDIATION_IMAGE = %v", m["value"])
			}
		}
	}
	if !found {
		t.Fatal("SELF_NODE_REMEDIATION_IMAGE env not rendered")
	}
}

func TestRenderFailsWhenImageMissing(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeyStorageBasedRemediation)
	imgs := testImages()
	delete(imgs, images.SBRAgent)
	if _, err := operands.Render(def, "wao-ns", imgs); err == nil || !strings.Contains(err.Error(), images.SBRAgent) {
		t.Fatalf("expected error naming %s, got %v", images.SBRAgent, err)
	}
}

func TestRenderLeavesNoPlaceholder(t *testing.T) {
	for _, def := range components.All() {
		objs, err := operands.Render(def, "wao-ns", testImages())
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			b, _ := o.MarshalJSON()
			if strings.Contains(string(b), operands.NamespacePlaceholder) {
				t.Errorf("%s: %s/%s still contains placeholder", def.Key, o.GetKind(), o.GetName())
			}
		}
	}
}

func TestRenderReplacesEveryOperandImage(t *testing.T) {
	for _, def := range components.All() {
		objs, err := operands.Render(def, "wao-ns", testImages())
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			b, _ := o.MarshalJSON()
			for _, env := range def.Images {
				if ph := images.Placeholder(env); strings.Contains(string(b), ph) {
					t.Errorf("%s: %s/%s still references placeholder %s", def.Key, o.GetKind(), o.GetName(), ph)
				}
			}
			if strings.Contains(string(b), "quay.io/medik8s/") {
				t.Errorf("%s: %s/%s references an unbound tagged image", def.Key, o.GetKind(), o.GetName())
			}
		}
	}
}

// NHC's manager creates a ServiceMonitor on OpenShift that assumes the operand's OCP packaging:
// a service-ca injected CA bundle, a metrics Service labelled instance=metrics with a serving
// certificate, and that certificate mounted where the manager enables metrics mTLS.
func TestRenderNHCMetricsWiringMatchesOCPPackaging(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	objs, err := operands.Render(def, testNamespace, testImages())
	if err != nil {
		t.Fatal(err)
	}
	ca := find(t, objs, "ConfigMap", "node-healthcheck-ca-bundle")
	if ca.GetAnnotations()["service.beta.openshift.io/inject-cabundle"] != "true" {
		t.Errorf("CA bundle annotations = %v", ca.GetAnnotations())
	}
	svc := find(t, objs, "Service", "node-healthcheck-controller-manager-metrics-service")
	if svc.GetLabels()["app.kubernetes.io/instance"] != "metrics" {
		t.Errorf("metrics Service labels = %v", svc.GetLabels())
	}
	if svc.GetAnnotations()["service.beta.openshift.io/serving-cert-secret-name"] != "node-healthcheck-tls" {
		t.Errorf("metrics Service annotations = %v", svc.GetAnnotations())
	}
	dep := find(t, objs, "Deployment", "node-healthcheck-controller-manager")
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	mounted := false
	for _, c := range containers {
		vms, _, _ := unstructured.NestedSlice(c.(map[string]interface{}), "volumeMounts")
		for _, vm := range vms {
			m := vm.(map[string]interface{})
			if m["mountPath"] == "/etc/tls/private" && m["readOnly"] == true {
				mounted = true
			}
		}
	}
	if !mounted {
		t.Error("manager has no read-only mount at /etc/tls/private")
	}
	vols, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
	backed := false
	for _, v := range vols {
		if secret, _, _ := unstructured.NestedString(v.(map[string]interface{}), "secret", "secretName"); secret == "node-healthcheck-tls" {
			backed = true
		}
	}
	if !backed {
		t.Errorf("no volume backed by Secret node-healthcheck-tls: %v", vols)
	}
}

// The OCP packaging also ships the Node Remediation console backend, which NHC registers as a
// ConsolePlugin at startup. WAO renders it as a second Deployment of the component, with its own
// image pinned by digest and without the manager's webhook certificate patch leaking into it.
func TestRenderNHCConsolePlugin(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	const name = "node-healthcheck-node-remediation-console-plugin"
	if !slices.Contains(def.Deployments, name) {
		t.Errorf("Deployments = %v, want %s included", def.Deployments, name)
	}
	objs, err := operands.Render(def, testNamespace, testImages())
	if err != nil {
		t.Fatal(err)
	}
	dep := find(t, objs, "Deployment", name)
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("console Deployment has %d containers, want 1: %v", len(containers), containers)
	}
	c := containers[0].(map[string]interface{})
	if c["name"] != "node-remediation-console-plugin" || c["image"] != testImages().Get(images.NHCConsole) {
		t.Errorf("console container = %v/%v", c["name"], c["image"])
	}
	vols, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
	for _, v := range vols {
		if secret, _, _ := unstructured.NestedString(v.(map[string]interface{}), "secret", "secretName"); strings.HasSuffix(secret, "-webhook-server-cert") {
			t.Errorf("webhook certificate volume leaked into the console Deployment: %v", v)
		}
	}
	svc := find(t, objs, "Service", name)
	if svc.GetAnnotations()["service.beta.openshift.io/serving-cert-secret-name"] != "nrc-plugin-cert" {
		t.Errorf("console Service annotations = %v", svc.GetAnnotations())
	}
}

func TestRenderLatestImagesPullPolicy(t *testing.T) {
	imgs := testImages()
	for env := range imgs {
		imgs[env] = "example.test/" + strings.ToLower(env) + ":latest"
	}
	for _, def := range components.All() {
		t.Run(def.Key, func(t *testing.T) {
			objs, err := operands.Render(def, testNamespace, imgs)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, obj := range objs {
				if obj.GetKind() != "Deployment" {
					continue
				}
				containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
				if err != nil {
					t.Fatal(err)
				}
				for _, raw := range containers {
					c := raw.(map[string]any)
					for _, env := range def.Images {
						if c["image"] == imgs[env] {
							seen[env] = true
							if c["imagePullPolicy"] != "Always" {
								t.Errorf("%s %s: latest policy = %v", obj.GetName(), env, c["imagePullPolicy"])
							}
						}
					}
					envs, _ := c["env"].([]any)
					for _, rawEnv := range envs {
						env := rawEnv.(map[string]any)
						var want string
						switch env["name"] {
						case "SELF_NODE_REMEDIATION_IMAGE":
							want = imgs[images.SNRManager]
						case "RELATED_IMAGE_AGENT":
							want = imgs[images.SBRAgent]
							seen[images.SBRAgent] = true
						}
						if want != "" && env["value"] != want {
							t.Errorf("%s = %v, want %s", env["name"], env["value"], want)
						}
					}
				}
			}
			for _, env := range def.Images {
				if !seen[env] {
					t.Errorf("image binding %s not exercised", env)
				}
			}
		})
	}
}

func TestRenderRejectsUnresolvedImagePlaceholder(t *testing.T) {
	def, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	def.Images = def.Images[:1] // the console placeholder stays in the manifests
	_, err := operands.Render(def, testNamespace, testImages())
	if err == nil || !strings.Contains(err.Error(), "unresolved placeholder") {
		t.Fatalf("err = %v", err)
	}
}
