// Package nmo registers the Node Maintenance Operator (NMO) admission webhook
// in the WAO manager. The webhook operates in each WAO replica, including when
// the NMO operand is Disabled. See docs/nmo-webhooks.md.
package nmo

import (
	"errors"
	"fmt"

	nmoutils "github.com/medik8s/node-maintenance-operator/v5/pkg/utils"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/listener"
	nmowebhookv1beta1 "github.com/medik8s/workload-availability-operator/internal/webhooks/nmo/v1beta1"
)

// NMO admission reads the Node of a NodeMaintenance. For a control plane node on OpenShift it lists
// the PodDisruptionBudgets and the Pods of the namespace openshift-etcd. The rules have the scope of
// the NMO ClusterRole (the cluster) and only the verbs that the handler calls. They have no write
// verb. test/offline compares the WAO role with this list. See decision N1 in docs/nmo-webhooks.md.
// The list of NodeMaintenances uses the permission of the WAO reconciler.
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get
// +kubebuilder:rbac:groups="",resources=pods,verbs=list
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=list

// Options holds the inputs of NMO admission.
type Options struct {
	// AdmissionClient reads Nodes, NodeMaintenances and the etcd objects for the validator.
	// It is mandatory and must not use a cache.
	AdmissionClient client.Client
	// IsOpenShift enables the etcd quorum rule for control plane nodes.
	IsOpenShift bool
}

// SetupWithManager registers the NodeMaintenance validator. Call it one time, before
// the manager starts. It registers no NMO controller.
func SetupWithManager(mgr ctrl.Manager, opts Options) error {
	switch {
	case mgr == nil:
		return errors.New("NMO admission: the manager is nil")
	case opts.AdmissionClient == nil:
		return errors.New("NMO admission: the admission client is nil")
	}
	if err := nmowebhookv1beta1.SetupNodeMaintenanceWebhookWithManager(opts.IsOpenShift, mgr, opts.AdmissionClient); err != nil {
		return fmt.Errorf("register the NodeMaintenance webhook: %w", err)
	}
	return nil
}

// Initialize prepares all inputs of NMO admission and registers the webhook.
// Each failure is a startup error: the caller must stop the manager, also when NMO is Disabled.
// The readiness check of the shared listener is not part of this function; see the listener package.
func Initialize(mgr ctrl.Manager) error {
	if mgr == nil {
		return errors.New("NMO admission: the manager is nil")
	}
	admissionClient, err := listener.NewAdmissionClient(mgr.GetConfig(), mgr.GetScheme())
	if err != nil {
		return err
	}
	isOpenShift, err := isOpenShift(mgr.GetConfig())
	if err != nil {
		return err
	}
	return SetupWithManager(mgr, Options{AdmissionClient: admissionClient, IsOpenShift: isOpenShift})
}

// isOpenShift uses the check of the NMO manager: API discovery, one time at start. Do not continue
// with an assumed answer when the discovery fails, because false disables the etcd quorum rule.
func isOpenShift(cfg *rest.Config) (bool, error) {
	validator, err := nmoutils.NewOpenshiftValidator(cfg)
	if err != nil {
		return false, fmt.Errorf("NMO admission: check if the cluster is OpenShift: %w", err)
	}
	return validator.IsOpenshiftSupported(), nil
}
