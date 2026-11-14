package components

import (
	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

func SNR() Definition {
	return Definition{
		Key: v1alpha1.ComponentKeySelfNodeRemediation, ShortName: "snr", ManifestDir: "snr",
		ManagerDeployment: "self-node-remediation-controller-manager",
		ManagerContainer:  "manager",
		Deployments:       []string{"self-node-remediation-controller-manager"},
		Images:            []string{images.SNRManager},
		CentralAdmission:  true,
		AgentOwners:       []ResourceKind{{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediationConfig"}},
		ManagedKinds:      []ResourceKind{{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation"}},
	}
}
