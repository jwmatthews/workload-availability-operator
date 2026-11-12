package components

import (
	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

func FAR() Definition {
	return Definition{
		Key: v1alpha1.ComponentKeyFenceAgentsRemediation, ShortName: "far", ManifestDir: "far",
		ManagerDeployment: "fence-agents-remediation-controller-manager",
		ManagerContainer:  "manager",
		Deployments:       []string{"fence-agents-remediation-controller-manager"},
		Images:            []string{images.FARManager},
		CentralAdmission:  true,
		ManagedKinds:      []ResourceKind{{Group: "fence-agents-remediation.medik8s.io", Version: "v1alpha1", Kind: "FenceAgentsRemediation"}},
	}
}
