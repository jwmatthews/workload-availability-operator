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
		ManagedKinds:      []ResourceKind{{Group: "remediation.medik8s.io", Version: "v1alpha1", Kind: "NodeHealthCheck"}},
	}
}
