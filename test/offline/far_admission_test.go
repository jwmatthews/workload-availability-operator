package offline

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const waoDeploymentName = "workload-availability-operator-controller-manager"

// webhookEntry is the part of an admission entry that must be equal in the
// Kubebuilder markers of the copied operand files and in the CSV.
type webhookEntry struct {
	Name, Type, Path, FailurePolicy, SideEffects string
	Rules                                        string
	AdmissionReviewVersions                      []string
}

// markerEntries reads config/webhook/manifests.yaml, which controller-gen writes from the
// markers of the copied operand webhook files (FAR and NHC).
func markerEntries(t *testing.T) map[string]webhookEntry {
	t.Helper()
	entries := map[string]webhookEntry{}
	add := func(kind, name string, cc admissionregistrationv1.WebhookClientConfig,
		rules []admissionregistrationv1.RuleWithOperations, failure *admissionregistrationv1.FailurePolicyType,
		side *admissionregistrationv1.SideEffectClass, versions []string) {
		if cc.Service == nil || cc.Service.Path == nil || failure == nil || side == nil {
			t.Fatalf("%s: incomplete marker entry", name)
		}
		entries[name] = webhookEntry{Name: name, Type: kind, Path: *cc.Service.Path, FailurePolicy: string(*failure),
			SideEffects: string(*side), Rules: fmt.Sprintf("%+v", rules), AdmissionReviewVersions: versions}
	}
	for _, doc := range strings.Split(readRepoFile(t, "config/webhook/manifests.yaml"), "\n---\n") {
		switch {
		case strings.Contains(doc, "kind: MutatingWebhookConfiguration"):
			cfg := &admissionregistrationv1.MutatingWebhookConfiguration{}
			if err := yaml.UnmarshalStrict([]byte(doc), cfg); err != nil {
				t.Fatal(err)
			}
			for _, w := range cfg.Webhooks {
				add("MutatingAdmissionWebhook", w.Name, w.ClientConfig, w.Rules, w.FailurePolicy, w.SideEffects,
					w.AdmissionReviewVersions)
			}
		case strings.Contains(doc, "kind: ValidatingWebhookConfiguration"):
			cfg := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			if err := yaml.UnmarshalStrict([]byte(doc), cfg); err != nil {
				t.Fatal(err)
			}
			for _, w := range cfg.Webhooks {
				add("ValidatingAdmissionWebhook", w.Name, w.ClientConfig, w.Rules, w.FailurePolicy, w.SideEffects,
					w.AdmissionReviewVersions)
			}
		}
	}
	return entries
}

// The table from docs/far-webhooks.md. The markers and the CSV must agree with it.
var farAdmissionTable = map[string][3]string{
	"mfenceagentsremediation.kb.io": {"MutatingAdmissionWebhook", "fenceagentsremediations",
		"/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation"},
	"vfenceagentsremediation.kb.io": {"ValidatingAdmissionWebhook", "fenceagentsremediations",
		"/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation"},
	"mfenceagentsremediationtemplate.kb.io": {"MutatingAdmissionWebhook", "fenceagentsremediationtemplates",
		"/mutate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate"},
	"vfenceagentsremediationtemplate.kb.io": {"ValidatingAdmissionWebhook", "fenceagentsremediationtemplates",
		"/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediationtemplate"},
}

func TestFARCSVHasFourCentralWebhookDefinitions(t *testing.T) {
	markers := markerEntries(t)
	for name := range farAdmissionTable {
		if _, found := markers[name]; !found {
			t.Fatalf("the copied FAR files have no webhook marker %s", name)
		}
	}
	wantRules := func(resource string) string {
		return fmt.Sprintf("%+v", []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
			Rule: admissionregistrationv1.Rule{APIGroups: []string{"fence-agents-remediation.medik8s.io"},
				APIVersions: []string{"v1alpha1"}, Resources: []string{resource}},
		}})
	}
	// The markers are the source. operator-sdk ignores webhook definitions in the base CSV.
	base := readResourceYAML(t, "config/manifests/bases/"+waoCSVFile)
	if _, set, _ := unstructured.NestedSlice(base, "spec", "webhookdefinitions"); set {
		t.Error("the base CSV has webhook definitions; bundle generation does not use them")
	}
	for _, path := range []string{"bundle/manifests/" + waoCSVFile} {
		t.Run(path, func(t *testing.T) {
			csv := readResourceYAML(t, path)
			definitions, _, err := unstructured.NestedSlice(csv, "spec", "webhookdefinitions")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, raw := range definitions {
				d := raw.(map[string]any)
				name, _ := d["generateName"].(string)
				if _, nhc := nhcAdmissionTable[name]; nhc {
					continue // nhc_admission_test.go checks the NHC definition.
				}
				row, known := farAdmissionTable[name]
				if !known || seen[name] {
					t.Fatalf("unexpected or repeated webhook definition %q", name)
				}
				seen[name] = true
				rulesYAML, err := yaml.Marshal(d["rules"])
				if err != nil {
					t.Fatal(err)
				}
				var rules []admissionregistrationv1.RuleWithOperations
				if err := yaml.UnmarshalStrict(rulesYAML, &rules); err != nil {
					t.Fatal(err)
				}
				versions, _, _ := unstructured.NestedStringSlice(d, "admissionReviewVersions")
				got := webhookEntry{Name: name, Type: fmt.Sprint(d["type"]), Path: fmt.Sprint(d["webhookPath"]),
					FailurePolicy: fmt.Sprint(d["failurePolicy"]), SideEffects: fmt.Sprint(d["sideEffects"]),
					Rules: fmt.Sprintf("%+v", rules), AdmissionReviewVersions: versions}
				want := webhookEntry{Name: name, Type: row[0], Path: row[2], FailurePolicy: "Fail", SideEffects: "None",
					Rules: wantRules(row[1]), AdmissionReviewVersions: []string{"v1"}}
				if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
					t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
				}
				if fmt.Sprintf("%+v", markers[name]) != fmt.Sprintf("%+v", want) {
					t.Errorf("%s: marker entry differs from the table:\n got %+v\nwant %+v", name, markers[name], want)
				}
				if d["deploymentName"] != waoDeploymentName {
					t.Errorf("%s: deploymentName = %v, want %s", name, d["deploymentName"], waoDeploymentName)
				}
				if fmt.Sprint(d["targetPort"]) != "9443" {
					t.Errorf("%s: targetPort = %v, want 9443", name, d["targetPort"])
				}
				notSet := []string{"objectSelector", "matchPolicy", "timeoutSeconds", "reinvocationPolicy", "conversionCRDs"}
				for _, absent := range notSet {
					if _, set := d[absent]; set {
						t.Errorf("%s: %s is set; FAR does not set it", name, absent)
					}
				}
			}
			if len(seen) != 4 {
				t.Fatalf("%d FAR webhook definitions, want 4", len(seen))
			}
		})
	}
}

// The webhook definitions must point at the Deployment that the bundle installs, and that
// Deployment must serve on the target port with the certificates that OLM mounts.
func TestFARWebhookDefinitionsTargetTheRenderedWAODeployment(t *testing.T) {
	csv := readResourceYAML(t, "bundle/manifests/"+waoCSVFile)
	deployments, _, _ := unstructured.NestedSlice(csv, "spec", "install", "spec", "deployments")
	var manager map[string]any
	for _, raw := range deployments {
		if d := raw.(map[string]any); d["name"] == waoDeploymentName {
			manager = d
		}
	}
	if manager == nil {
		t.Fatalf("the bundle CSV installs no Deployment %s", waoDeploymentName)
	}
	containers, _, _ := unstructured.NestedSlice(manager, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	ports, _, _ := unstructured.NestedSlice(container, "ports")
	found := false
	for _, raw := range ports {
		if p := raw.(map[string]any); fmt.Sprint(p["containerPort"]) == "9443" && p["name"] == "webhook-server" {
			found = true
		}
	}
	if !found {
		t.Errorf("the manager container does not declare port 9443 (webhook-server): %v", ports)
	}
	args, _, _ := unstructured.NestedStringSlice(container, "args")
	if !slices.Contains(args, "--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs") {
		t.Errorf("the manager args %q do not select the certificate directory that OLM mounts", args)
	}
	// OLM creates the certificate volume. The bundle must not carry a competing volume or cert-manager object.
	volumes, _, _ := unstructured.NestedSlice(manager, "spec", "template", "spec", "volumes")
	for _, raw := range volumes {
		if v := raw.(map[string]any); v["secret"] != nil {
			t.Errorf("the WAO Deployment has a Secret volume %v; OLM supplies the serving certificate", v["name"])
		}
	}
}

// WAO never renders or reconciles the central registration.
func TestFARRuntimeManifestsHaveNoAdmissionConfiguration(t *testing.T) {
	objects := readRepoFile(t, "internal/operands/manifests/far/objects.yaml")
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		if strings.Contains(objects, "kind: "+kind) {
			t.Errorf("FAR runtime manifests contain a %s", kind)
		}
	}
	// The unchanged FAR image still starts its webhook listener, so its Service and certificate stay.
	kept := []string{"name: fence-agents-remediation-webhook-service", "fence-agents-remediation-webhook-server-cert"}
	for _, want := range kept {
		if !strings.Contains(objects, want) {
			t.Errorf("FAR runtime manifests lost %q", want)
		}
	}
	for _, key := range []string{"nhc", "snr", "sbr", "mdr", "nmo"} {
		objects := readRepoFile(t, "internal/operands/manifests/"+key+"/objects.yaml")
		if strings.Contains(objects, "fence-agents-remediation.medik8s.io") {
			t.Errorf("%s runtime manifests refer to the FAR API group", key)
		}
	}
}

// FAR admission reads Secrets. The WAO ServiceAccount gets cluster-wide get and no other Secret verb.
// Operand ServiceAccounts have their own permissions; this check does not include them.
func TestWAOServiceAccountSecretPermissionIsGetOnly(t *testing.T) {
	csv := readResourceYAML(t, "bundle/manifests/"+waoCSVFile)
	secretRules, deploymentRules := 0, 0
	for _, scope := range []string{"clusterPermissions", "permissions"} {
		permissions, _, _ := unstructured.NestedSlice(csv, "spec", "install", "spec", scope)
		for _, raw := range permissions {
			p := raw.(map[string]any)
			if p["serviceAccountName"] != waoDeploymentName {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(p, "rules")
			for _, rawRule := range rules {
				rule := rawRule.(map[string]any)
				groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
				coreGroup := slices.Contains(groups, "") || slices.Contains(groups, "*")
				coversSecrets := slices.ContainsFunc(resources, func(r string) bool {
					return r == "*" || r == "secrets" || strings.HasPrefix(r, "secrets/")
				})
				if coreGroup && coversSecrets {
					secretRules++
					getOnly := slices.Equal(verbs, []string{"get"}) && slices.Equal(resources, []string{"secrets"})
					if scope != "clusterPermissions" || !getOnly {
						t.Errorf("%s rule for Secrets: resources %v verbs %v, want cluster-wide get on secrets only",
							scope, resources, verbs)
					}
				}
				if slices.Contains(groups, "apps") && slices.Contains(resources, "deployments") &&
					slices.Contains(verbs, "create") {
					deploymentRules++
				}
			}
		}
	}
	if secretRules != 1 {
		t.Errorf("%d Secret rules for the WAO ServiceAccount, want 1", secretRules)
	}
	if deploymentRules == 0 {
		t.Error("the WAO ServiceAccount lost its Deployment permissions")
	}
}
