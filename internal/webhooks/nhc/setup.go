// Package nhc registers the Node Healthcheck Operator (NHC) admission webhook
// in the WAO manager. The webhook operates in each WAO replica, including when
// the NHC operand is Disabled. See docs/nhc-webhooks.md.
package nhc

import (
	"context"
	"errors"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/listener"
	"github.com/medik8s/workload-availability-operator/internal/webhooks/nhc/cluster"
	nhcwebhookv1alpha1 "github.com/medik8s/workload-availability-operator/internal/webhooks/nhc/v1alpha1"
)

// NHC admission reads the control plane topology one time at start. The RBAC marker for the
// Infrastructure object is in the copied file cluster/capabilities.go (get, list, watch, as in NHC).

// NHC admission lists the templates of one kind when a NodeHealthCheck has two escalating
// remediations of that kind. Only the template kinds in the WAO bundle are here; test/offline
// compares this list with the bundle. See docs/nhc-webhooks.md for other template kinds.
// +kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=list
// +kubebuilder:rbac:groups=fence-agents-remediation.medik8s.io,resources=fenceagentsremediationtemplates,verbs=list
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediationtemplates,verbs=list
// +kubebuilder:rbac:groups=machine-deletion-remediation.medik8s.io,resources=machinedeletionremediationtemplates,verbs=list

// capabilitiesTimeout limits the read of the cluster capabilities at start.
const capabilitiesTimeout = 30 * time.Second

// Options holds the inputs of NHC admission. All fields are mandatory.
type Options struct {
	// AdmissionClient lists remediation templates for the validator. It must not use a cache.
	AdmissionClient client.Client
	// Capabilities tells if the control plane topology of the cluster is a supported one.
	Capabilities *cluster.Capabilities
}

// SetupWithManager registers the NodeHealthCheck validator. Call it one time, before
// the manager starts. It registers no NHC controller.
func SetupWithManager(mgr ctrl.Manager, opts Options) error {
	switch {
	case mgr == nil:
		return errors.New("NHC admission: the manager is nil")
	case opts.AdmissionClient == nil:
		return errors.New("NHC admission: the admission client is nil")
	case opts.Capabilities == nil:
		return errors.New("NHC admission: the cluster capabilities are nil")
	}
	if err := nhcwebhookv1alpha1.SetupWebhookWithManager(mgr, opts.AdmissionClient, opts.Capabilities); err != nil {
		return fmt.Errorf("register the NodeHealthCheck webhook: %w", err)
	}
	return nil
}

// Initialize prepares all inputs of NHC admission and registers the webhook.
// Each failure is a startup error: the caller must stop the manager, also when NHC is Disabled.
// The readiness check of the shared listener is not part of this function; see the listener package.
func Initialize(mgr ctrl.Manager) error {
	if mgr == nil {
		return errors.New("NHC admission: the manager is nil")
	}
	admissionClient, err := listener.NewAdmissionClient(mgr.GetConfig(), mgr.GetScheme())
	if err != nil {
		return err
	}
	// The manager cache does not operate before the manager starts, so the dedicated client reads the
	// Infrastructure object. Do not continue with an assumed topology when the read fails.
	ctx, cancel := context.WithTimeout(context.Background(), capabilitiesTimeout)
	defer cancel()
	caps, err := cluster.NewCapabilities(mgr.GetConfig(), admissionClient, ctrl.Log.WithName("nhc-admission"), ctx)
	if err != nil {
		return fmt.Errorf("NHC admission: read the cluster capabilities: %w", err)
	}
	return SetupWithManager(mgr, Options{AdmissionClient: admissionClient, Capabilities: &caps})
}
