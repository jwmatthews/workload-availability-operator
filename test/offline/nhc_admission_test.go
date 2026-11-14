package offline

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// The table from docs/nhc-webhooks.md. The marker and the CSV must agree with it.
var nhcAdmissionTable = map[string][3]string{
	"vnodehealthcheck.kb.io": {"ValidatingAdmissionWebhook", "nodehealthchecks",
		"/validate-remediation-medik8s-io-v1alpha1-nodehealthcheck"},
}

// aggregationLabel marks the ClusterRoles that standalone NHC aggregates for access to remediators.
const aggregationLabel = "rbac.ext-remediation/aggregate-to-ext-remediation"

// csvWebhookDefinitions returns the webhook definitions of the bundle CSV.
func csvWebhookDefinitions(t *testing.T) []map[string]any {
	t.Helper()
	csv := readResourceYAML(t, "bundle/manifests/"+waoCSVFile)
	raw, _, err := unstructured.NestedSlice(csv, "spec", "webhookdefinitions")
	if err != nil {
		t.Fatal(err)
	}
	definitions := make([]map[string]any, 0, len(raw))
	for _, d := range raw {
		definitions = append(definitions, d.(map[string]any))
	}
	return definitions
}

// Each marker of the copied operand files has one CSV definition, and the CSV has no other definition.
func TestCSVHasOneWebhookDefinitionForEachMarker(t *testing.T) {
	markers := markerEntries(t)
	want := len(farAdmissionTable) + len(nhcAdmissionTable) + len(nmoAdmissionTable) + len(snrAdmissionTable)
	if len(markers) != want {
		t.Fatalf("the copied operand files have %d webhook markers, want %d", len(markers), want)
	}
	seen := map[string]bool{}
	for _, d := range csvWebhookDefinitions(t) {
		name, _ := d["generateName"].(string)
		if _, known := markers[name]; !known || seen[name] {
			t.Errorf("unexpected or repeated webhook definition %q", name)
		}
		seen[name] = true
	}
	if len(seen) != want {
		t.Errorf("the CSV has %d webhook definitions, want %d", len(seen), want)
	}
}

func TestNHCCSVHasOneCentralWebhookDefinition(t *testing.T) {
	const name = "vnodehealthcheck.kb.io"
	row := nhcAdmissionTable[name]
	// NHC also validates DELETE, which FAR does not.
	want := webhookEntry{Name: name, Type: row[0], Path: row[2], FailurePolicy: "Fail", SideEffects: "None",
		Rules: fmt.Sprintf("%+v", []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create,
				admissionregistrationv1.Update, admissionregistrationv1.Delete},
			Rule: admissionregistrationv1.Rule{APIGroups: []string{"remediation.medik8s.io"},
				APIVersions: []string{"v1alpha1"}, Resources: []string{row[1]}},
		}}), AdmissionReviewVersions: []string{"v1"}}

	marker, found := markerEntries(t)[name]
	if !found {
		t.Fatalf("the copied NHC file has no webhook marker %s", name)
	}
	if fmt.Sprintf("%+v", marker) != fmt.Sprintf("%+v", want) {
		t.Errorf("marker entry differs from the table:\n got %+v\nwant %+v", marker, want)
	}

	checkCSVDefinition(t, want, "NHC", 0)
}

// checkCSVDefinition checks that the bundle CSV has exactly one definition with the name of want,
// that it agrees with want, and that it points at the WAO Deployment. timeoutSeconds is the timeout
// that the operand sets for the entry; 0 means that the operand sets none (the API server default).
func checkCSVDefinition(t *testing.T, want webhookEntry, operand string, timeoutSeconds int64) {
	t.Helper()
	name := want.Name
	count := 0
	for _, d := range csvWebhookDefinitions(t) {
		if d["generateName"] != name {
			continue
		}
		count++
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
		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
			t.Errorf("CSV definition:\n got %+v\nwant %+v", got, want)
		}
		if d["deploymentName"] != waoDeploymentName {
			t.Errorf("deploymentName = %v, want %s", d["deploymentName"], waoDeploymentName)
		}
		if fmt.Sprint(d["targetPort"]) != "9443" {
			t.Errorf("targetPort = %v, want 9443", d["targetPort"])
		}
		notSet := []string{"objectSelector", "matchPolicy", "reinvocationPolicy", "conversionCRDs"}
		if timeoutSeconds == 0 {
			notSet = append(notSet, "timeoutSeconds")
		} else if fmt.Sprint(d["timeoutSeconds"]) != fmt.Sprint(timeoutSeconds) {
			t.Errorf("timeoutSeconds = %v, want %d as %s sets it", d["timeoutSeconds"], timeoutSeconds, operand)
		}
		for _, absent := range notSet {
			if _, set := d[absent]; set {
				t.Errorf("%s is set; %s does not set it", absent, operand)
			}
		}
	}
	if count != 1 {
		t.Errorf("%d CSV definitions with the name %s, want 1", count, name)
	}
}

// waoClusterRules returns the verbs of the WAO ServiceAccount for each "group/resource" in the bundle CSV.
func waoClusterRules(t *testing.T) map[string][]string {
	t.Helper()
	csv := readResourceYAML(t, "bundle/manifests/"+waoCSVFile)
	out := map[string][]string{}
	permissions, _, _ := unstructured.NestedSlice(csv, "spec", "install", "spec", "clusterPermissions")
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
			for _, g := range groups {
				for _, r := range resources {
					out[g+"/"+r] = append(out[g+"/"+r], verbs...)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the bundle CSV has no cluster permissions for the WAO ServiceAccount")
	}
	return out
}

// NHC admission lists remediation templates. Standalone NHC gets that permission from the
// ClusterRoles with the aggregation label. WAO has a static list instead (decision D2 of the
// NHC webhook plan). This test fails when the bundle gets a remediator that the list does not have.
func TestWAOServiceAccountListsEachBundledTemplateKind(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "bundle", "manifests", "*clusterrole.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wao := waoClusterRules(t)
	templates := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		role := map[string]any{}
		if err := yaml.Unmarshal(data, &role); err != nil {
			t.Fatal(err)
		}
		labels, _, _ := unstructured.NestedStringMap(role, "metadata", "labels")
		if labels[aggregationLabel] != "true" {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(role, "rules")
		for _, rawRule := range rules {
			rule := rawRule.(map[string]any)
			groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
			resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
			for _, g := range groups {
				for _, r := range resources {
					if !strings.HasSuffix(r, "templates") {
						continue
					}
					templates++
					// Admission needs list only. It must not get a write verb on a template.
					if verbs := wao[g+"/"+r]; !slices.Equal(verbs, []string{"list"}) {
						t.Errorf("WAO ServiceAccount verbs for %s/%s = %v, want [list] (%s)", g, r, verbs, filepath.Base(file))
					}
				}
			}
		}
	}
	if templates != 4 {
		t.Errorf("the bundle has %d remediation template kinds in labelled ClusterRoles, want 4", templates)
	}
}

// The copied NHC file cluster/capabilities.go reads the Infrastructure object. WAO gets read access only.
func TestWAOServiceAccountReadsInfrastructureOnly(t *testing.T) {
	verbs := waoClusterRules(t)["config.openshift.io/infrastructures"]
	if !slices.Equal(verbs, []string{"get", "list", "watch"}) {
		t.Errorf("WAO ServiceAccount verbs for infrastructures = %v, want [get list watch]", verbs)
	}
}

// WAO never renders or reconciles the central registration.
func TestNHCRuntimeManifestsHaveNoAdmissionConfiguration(t *testing.T) {
	objects := readRepoFile(t, "internal/operands/manifests/nhc/objects.yaml")
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		if strings.Contains(objects, "kind: "+kind) {
			t.Errorf("NHC runtime manifests contain a %s", kind)
		}
	}
	// The unchanged NHC image still starts its webhook listener, so its Service and certificate stay.
	for _, want := range []string{"name: node-healthcheck-webhook-service", "node-healthcheck-webhook-server-cert"} {
		if !strings.Contains(objects, want) {
			t.Errorf("NHC runtime manifests lost %q", want)
		}
	}
}
