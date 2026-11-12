// Package far registers the Fence Agents Remediation (FAR) admission webhooks
// in the WAO manager. The webhooks operate in each WAO replica, including when
// the FAR operand is Disabled. See docs/far-webhooks.md.
package far

import (
	"errors"
	"fmt"

	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"
	"github.com/medik8s/fence-agents-remediation/v5/pkg/validation"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/far/inventory"
	farwebhookv1alpha1 "github.com/medik8s/workload-availability-operator/internal/webhooks/far/v1alpha1"
)

// FAR admission reads the Secrets that hold fence agent parameters, in any namespace.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Options holds the inputs of FAR admission. All fields except OutOfServiceTaintSupported are mandatory.
type Options struct {
	// AgentExists reports if the selected FAR image contains a fence agent.
	AgentExists validation.AgentExists
	// AdmissionClient reads Secrets for the validators and the defaulters. It must not use a cache.
	AdmissionClient client.Client
	// OutOfServiceTaintSupported tells if the cluster supports the OutOfServiceTaint strategy.
	OutOfServiceTaintSupported bool
}

// SetupWithManager sets the FAR package state and registers the validator and the
// defaulter of both FAR resources. Call it one time, before the manager starts.
// It registers no FAR controller and executes no fence agent.
func SetupWithManager(mgr ctrl.Manager, opts Options) error {
	switch {
	case mgr == nil:
		return errors.New("FAR admission: the manager is nil")
	case opts.AgentExists == nil:
		return errors.New("FAR admission: the agent checker is nil")
	case opts.AdmissionClient == nil:
		return errors.New("FAR admission: the admission client is nil")
	}

	// The imported FAR validators read this package state. Set it before any request can arrive.
	farapi.SetAgentValidator(validation.NewCustomAgentValidator(opts.AgentExists))
	farapi.SetOutOfServiceTaintSupported(opts.OutOfServiceTaintSupported)

	if err := farwebhookv1alpha1.SetupFARWebhookWithManager(mgr, opts.AdmissionClient); err != nil {
		return fmt.Errorf("register FenceAgentsRemediation webhooks: %w", err)
	}
	if err := farwebhookv1alpha1.SetupFARTemplateWebhookWithManager(mgr, opts.AdmissionClient); err != nil {
		return fmt.Errorf("register FenceAgentsRemediationTemplate webhooks: %w", err)
	}
	return nil
}

// Initialize prepares all inputs of FAR admission and registers the webhooks.
// Each failure is a startup error: the caller must stop the manager, also when FAR is Disabled.
func Initialize(mgr ctrl.Manager, inventoryPath string) error {
	if mgr == nil {
		return errors.New("FAR admission: the manager is nil")
	}
	agentExists, err := inventory.Load(inventoryPath)
	if err != nil {
		return err
	}
	// Do not continue with an assumed value when discovery fails.
	taintValidator, err := validation.NewOutOfServiceTaintValidator(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("FAR admission: discover the Kubernetes version: %w", err)
	}
	admissionClient, err := NewAdmissionClient(mgr.GetConfig(), mgr.GetScheme())
	if err != nil {
		return err
	}
	if err := SetupWithManager(mgr, Options{
		AgentExists:                agentExists,
		AdmissionClient:            admissionClient,
		OutOfServiceTaintSupported: taintValidator.IsOutOfServiceTaintSupported(),
	}); err != nil {
		return err
	}
	// The check uses only the local listener. It does not use the FAR Deployment or the WAO Service endpoints.
	if err := mgr.AddReadyzCheck(ReadyCheckName, mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("FAR admission: add the readiness check: %w", err)
	}
	return nil
}

// ReadyCheckName is the name of the readiness check of the local webhook listener.
const ReadyCheckName = "far-admission"

// Ready returns a function that reports if the local webhook listener of this replica serves.
func Ready(mgr ctrl.Manager) func() bool {
	check := mgr.GetWebhookServer().StartedChecker()
	return func() bool { return check(nil) == nil }
}
