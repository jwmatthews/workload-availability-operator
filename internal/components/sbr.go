package components

import (
	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

func SBR() Definition {
	return Definition{
		Key: v1alpha1.ComponentKeyStorageBasedRemediation, ShortName: "sbr", ManifestDir: "sbr",
		ManagerDeployment:      "sbr-operator-controller-manager",
		ManagerContainer:       "manager",
		Deployments:            []string{"sbr-operator-controller-manager"},
		Images:                 []string{images.SBRManager, images.SBRAgent},
		AgentOwners:            []ResourceKind{{Group: "storage-based-remediation.medik8s.io", Version: "v1alpha1", Kind: "StorageBasedRemediationConfig"}},
		AgentsInAllNamespaces:  true,
		ManagedKinds:           []ResourceKind{{Group: "storage-based-remediation.medik8s.io", Version: "v1alpha1", Kind: "StorageBasedRemediation"}},
		AgentDaemonSetSelector: map[string]string{"app": "sbr-agent", "managed-by": "sbr-operator"},
	}
}
