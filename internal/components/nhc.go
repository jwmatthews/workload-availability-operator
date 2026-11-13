package components

import (
	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

func NHC() Definition {
	return Definition{
		Key: v1alpha1.ComponentKeyNodeHealthCheck, ShortName: "nhc", ManifestDir: "nhc",
		ManagerDeployment: "node-healthcheck-controller-manager",
		ManagerContainer:  "manager",
		Deployments:       []string{"node-healthcheck-controller-manager", "node-healthcheck-node-remediation-console-plugin"},
		Images:            []string{images.NHCManager, images.NHCConsole},
		CentralAdmission:  true,
		AdmissionBlock:    &AdmissionBlock{Blocked: nhcRemediating, Message: nhcBlockMessage},
		ManagedKinds:      []ResourceKind{{Group: "remediation.medik8s.io", Version: "v1alpha1", Kind: "NodeHealthCheck"}},
	}
}

// nhcBlockMessage is the report for a NodeHealthCheck that nhcRemediating selects.
const nhcBlockMessage = "the status shows a remediation, so admission rejects the delete and a change of the selector, " +
	"the remediation template or the escalating remediations; set the component to Enabled again, " +
	"or remove status.unhealthyNodes with a patch to the status subresource"

// nhcRemediating reports if the status of a NodeHealthCheck shows a remediation. It is the condition of
// isRemediating in the copied NHC webhook (internal/webhooks/nhc/v1alpha1), which rejects the delete and
// the change of some spec fields. A test in internal/webhooks/nhc compares the two.
func nhcRemediating(object map[string]any) bool {
	status, _ := object["status"].(map[string]any)
	nodes, _ := status["unhealthyNodes"].([]any)
	for _, node := range nodes {
		entry, _ := node.(map[string]any)
		if remediations, _ := entry["remediations"].([]any); len(remediations) > 0 {
			return true
		}
	}
	return false
}
