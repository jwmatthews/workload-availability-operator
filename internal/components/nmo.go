package components

import (
	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

func NMO() Definition {
	return Definition{
		Key: v1alpha1.ComponentKeyNodeMaintenance, ShortName: "nmo", ManifestDir: "nmo",
		ManagerDeployment: "node-maintenance-operator-controller-manager",
		ManagerContainer:  "manager",
		Deployments:       []string{"node-maintenance-operator-controller-manager"},
		Images:            []string{images.NMOManager},
		CentralAdmission:  true,
		ManagedKinds:      []ResourceKind{{Group: "nodemaintenance.medik8s.io", Version: "v1beta1", Kind: "NodeMaintenance"}},
	}
}
