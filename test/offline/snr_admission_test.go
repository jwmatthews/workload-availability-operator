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

// The table from docs/snr-webhooks.md: type, resource, path, failurePolicy, operations.
// The marker and the CSV must agree with it.
var snrAdmissionTable = map[string][5]string{
	"mselfnoderemediationtemplate.kb.io": {"MutatingAdmissionWebhook", "selfnoderemediationtemplates",
		"/mutate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate", "Fail", "CREATE,UPDATE"},
	"vselfnoderemediationtemplate.kb.io": {"ValidatingAdmissionWebhook", "selfnoderemediationtemplates",
		"/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate", "Fail", "CREATE,UPDATE"},
	"vselfnoderemediationconfig.kb.io": {"ValidatingAdmissionWebhook", "selfnoderemediationconfigs",
		"/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig", "Fail", "CREATE,UPDATE,DELETE"},
	// A remediation must not depend on the admission listener, so SNR selects Ignore for this entry.
	"vselfnoderemediation.kb.io": {"ValidatingAdmissionWebhook", "selfnoderemediations",
		"/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation", "Ignore", "CREATE,UPDATE"},
}

func TestSNRCSVHasFourCentralWebhookDefinitions(t *testing.T) {
	markers := markerEntries(t)
	for name, row := range snrAdmissionTable {
		t.Run(name, func(t *testing.T) {
			names := strings.Split(row[4], ",")
			operations := make([]admissionregistrationv1.OperationType, 0, len(names))
			for _, op := range names {
				operations = append(operations, admissionregistrationv1.OperationType(op))
			}
			want := webhookEntry{Name: name, Type: row[0], Path: row[2], FailurePolicy: row[3], SideEffects: "None",
				Rules: fmt.Sprintf("%+v", []admissionregistrationv1.RuleWithOperations{{
					Operations: operations,
					Rule: admissionregistrationv1.Rule{APIGroups: []string{"self-node-remediation.medik8s.io"},
						APIVersions: []string{"v1alpha1"}, Resources: []string{row[1]}},
				}}), AdmissionReviewVersions: []string{"v1"}}
			marker, found := markers[name]
			if !found {
				t.Fatalf("the copied SNR files have no webhook marker %s", name)
			}
			if fmt.Sprintf("%+v", marker) != fmt.Sprintf("%+v", want) {
				t.Errorf("marker entry differs from the table:\n got %+v\nwant %+v", marker, want)
			}
			// SNR sets no timeout for its entries, so the API server default applies.
			checkCSVDefinition(t, want, "SNR", 0)
		})
	}
}

// WAO must not change the failurePolicy of an SNR entry (AGENTS.md, decision S1 of the SNR webhook
// plan). The source of the comparison is the copied SNR configuration, not the table of this file.
func TestSNRCSVKeepsTheFailurePolicyOfEachSNREntry(t *testing.T) {
	upstream := map[string]string{}
	for _, doc := range strings.Split(readRepoFile(t, "config/operands/snr/config/webhook/manifests.yaml"), "\n---\n") {
		var configuration struct {
			Webhooks []struct {
				Name          string `json:"name"`
				FailurePolicy string `json:"failurePolicy"`
				// A timeout in the copied configuration needs a patch in config/webhook/kustomization.yaml.
				TimeoutSeconds *int32 `json:"timeoutSeconds"`
			} `json:"webhooks"`
		}
		if err := yaml.Unmarshal([]byte(doc), &configuration); err != nil {
			t.Fatal(err)
		}
		for _, webhook := range configuration.Webhooks {
			upstream[webhook.Name] = webhook.FailurePolicy
			if webhook.TimeoutSeconds != nil {
				t.Errorf("the copied SNR entry %s sets a timeout; keep it in the WAO registration", webhook.Name)
			}
		}
	}
	if len(upstream) != len(snrAdmissionTable) {
		t.Fatalf("the copied SNR configuration has %d entries, want %d: %v", len(upstream), len(snrAdmissionTable), upstream)
	}
	if strings.Contains(readRepoFile(t, "config/operands/snr/config/webhook/kustomization.yaml"), "patches") {
		t.Error("the copied SNR webhook kustomization has a patch; compare it with config/webhook/kustomization.yaml")
	}
	ignored := 0
	for _, d := range csvWebhookDefinitions(t) {
		name, _ := d["generateName"].(string)
		want, isSNR := upstream[name]
		if !isSNR {
			continue
		}
		if d["failurePolicy"] != want {
			t.Errorf("%s: failurePolicy = %v in the CSV, SNR has %s", name, d["failurePolicy"], want)
		}
		if want == "Ignore" {
			ignored++
		}
		delete(upstream, name)
	}
	if len(upstream) != 0 {
		t.Errorf("the CSV has no definition for the SNR entries %v", upstream)
	}
	if ignored != 1 {
		t.Errorf("%d SNR entries with Ignore, want 1 (vselfnoderemediation.kb.io)", ignored)
	}
}

// SNR admission reads no object from the cluster, so it adds no permission. The WAO ServiceAccount
// keeps the read verbs of the WAO reconciler on the SNR kinds and gets no write verb.
func TestWAOServiceAccountHasNoNewVerbForSNRAdmission(t *testing.T) {
	wao := waoClusterRules(t)
	for resource, want := range map[string][]string{
		"self-node-remediation.medik8s.io/selfnoderemediationconfigs":   {"get", "list", "watch"},
		"self-node-remediation.medik8s.io/selfnoderemediations":         {"get", "list", "watch"},
		"self-node-remediation.medik8s.io/selfnoderemediationtemplates": {"list"},
	} {
		got := slices.Clone(wao[resource])
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("WAO verbs on %s = %v, want %v", resource, got, want)
		}
	}
	for resource := range wao {
		if strings.HasPrefix(resource, "self-node-remediation.medik8s.io/") && strings.Contains(resource, "/status") {
			t.Errorf("the WAO ServiceAccount has a rule for %s", resource)
		}
	}
}

// The copied SNR config validator reads the namespace from OPERATOR_NAMESPACE (decision S2).
// The WAO Deployment must set it from the namespace of the Pod.
func TestWAODeploymentSetsTheNamespaceForSNRAdmission(t *testing.T) {
	source := readRepoFile(t, "internal/webhooks/snr/utils/namespaces.go")
	if !strings.Contains(source, `deployNamespaceEnvVar = "OPERATOR_NAMESPACE"`) {
		t.Fatal("the copied namespaces.go does not read OPERATOR_NAMESPACE")
	}
	csv := readResourceYAML(t, "bundle/manifests/"+waoCSVFile)
	deployments, _, _ := unstructured.NestedSlice(csv, "spec", "install", "spec", "deployments")
	found := false
	for _, raw := range deployments {
		deployment := raw.(map[string]any)
		if deployment["name"] != waoDeploymentName {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(deployment, "spec", "template", "spec", "containers")
		for _, rawContainer := range containers {
			env, _, _ := unstructured.NestedSlice(rawContainer.(map[string]any), "env")
			for _, rawEnv := range env {
				variable := rawEnv.(map[string]any)
				if variable["name"] != "OPERATOR_NAMESPACE" {
					continue
				}
				path, _, _ := unstructured.NestedString(variable, "valueFrom", "fieldRef", "fieldPath")
				if path != "metadata.namespace" {
					t.Errorf("OPERATOR_NAMESPACE comes from %q, want metadata.namespace", path)
				}
				found = true
			}
		}
	}
	if !found {
		t.Error("the WAO Deployment in the CSV does not set OPERATOR_NAMESPACE")
	}
}

// WAO never renders or reconciles the central registration.
func TestSNRRuntimeManifestsHaveNoAdmissionConfiguration(t *testing.T) {
	objects := readRepoFile(t, "internal/operands/manifests/snr/objects.yaml")
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		if strings.Contains(objects, "kind: "+kind) {
			t.Errorf("SNR runtime manifests contain a %s", kind)
		}
	}
	// The unchanged SNR image still starts its webhook listener, so its Service and certificate stay.
	kept := []string{"name: self-node-remediation-webhook-service", "self-node-remediation-webhook-server-cert"}
	for _, want := range kept {
		if !strings.Contains(objects, want) {
			t.Errorf("SNR runtime manifests lost %q", want)
		}
	}
}
