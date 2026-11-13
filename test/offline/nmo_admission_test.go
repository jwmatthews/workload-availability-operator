package offline

import (
	"fmt"
	"slices"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// The table from docs/nmo-webhooks.md. The marker and the CSV must agree with it.
var nmoAdmissionTable = map[string][3]string{
	"vnodemaintenance.kb.io": {"ValidatingAdmissionWebhook", "nodemaintenances",
		"/validate-nodemaintenance-medik8s-io-v1beta1-nodemaintenance"},
}

func TestNMOCSVHasOneCentralWebhookDefinition(t *testing.T) {
	const name = "vnodemaintenance.kb.io"
	row := nmoAdmissionTable[name]
	// NMO validates CREATE and UPDATE. It does not validate DELETE.
	want := webhookEntry{Name: name, Type: row[0], Path: row[2], FailurePolicy: "Fail", SideEffects: "None",
		Rules: fmt.Sprintf("%+v", []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
			Rule: admissionregistrationv1.Rule{APIGroups: []string{"nodemaintenance.medik8s.io"},
				APIVersions: []string{"v1beta1"}, Resources: []string{row[1]}},
		}}), AdmissionReviewVersions: []string{"v1"}}

	marker, found := markerEntries(t)[name]
	if !found {
		t.Fatalf("the copied NMO file has no webhook marker %s", name)
	}
	if fmt.Sprintf("%+v", marker) != fmt.Sprintf("%+v", want) {
		t.Errorf("marker entry differs from the table:\n got %+v\nwant %+v", marker, want)
	}

	checkCSVDefinition(t, want, "NMO")
}

// NMO admission reads a Node and lists the Pods and PodDisruptionBudgets of openshift-etcd.
// Decision N1 of the NMO webhook plan: cluster scope as in the NMO ClusterRole, only the verbs
// that the handler calls, and no write verb. A write verb on nodes or pods/eviction permits a
// cordon and a drain of a node, which admission must not do.
func TestWAOServiceAccountHasMinimumVerbsForNMOAdmission(t *testing.T) {
	wao := waoClusterRules(t)
	for resource, want := range map[string][]string{
		"/nodes":                      {"get"},
		"/pods":                       {"list"},
		"policy/poddisruptionbudgets": {"list"},
	} {
		if verbs := wao[resource]; !slices.Equal(verbs, want) {
			t.Errorf("WAO ServiceAccount verbs for %q = %v, want %v", resource, verbs, want)
		}
	}
	for _, resource := range []string{"/pods/eviction", "/nodes/status", "/pods/*", "/*", "*/*"} {
		if verbs, set := wao[resource]; set {
			t.Errorf("WAO ServiceAccount has verbs %v for %q, want no rule", verbs, resource)
		}
	}
	// The handler lists NodeMaintenances. The WAO reconciler reads them also. No write verb is necessary.
	verbs := wao["nodemaintenance.medik8s.io/nodemaintenances"]
	if !slices.Equal(verbs, []string{"get", "list", "watch"}) {
		t.Errorf("WAO ServiceAccount verbs for nodemaintenances = %v, want [get list watch]", verbs)
	}
}
