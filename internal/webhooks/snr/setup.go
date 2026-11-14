// Package snr registers the Self Node Remediation (SNR) admission webhooks
// in the WAO manager. The webhooks operate in each WAO replica, including when
// the SNR operand is Disabled. See docs/snr-webhooks.md.
package snr

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/snr/utils"
	snrwebhookv1alpha1 "github.com/medik8s/workload-availability-operator/internal/webhooks/snr/v1alpha1"
)

// SNR admission reads no object from the cluster. Thus it has no RBAC marker and no dedicated client.

// SetupWithManager registers the SelfNodeRemediationConfig validator, the SelfNodeRemediationTemplate
// defaulter and validator, and the SelfNodeRemediation validator, in the order of the SNR manager.
// Call it one time, before the manager starts. It registers no SNR controller.
func SetupWithManager(mgr ctrl.Manager) error {
	if mgr == nil {
		return errors.New("SNR admission: the manager is nil")
	}
	for _, webhook := range []struct {
		kind  string
		setup func(ctrl.Manager) error
	}{
		{"SelfNodeRemediationConfig", snrwebhookv1alpha1.SetupSNRConfigWebhookWithManager},
		{"SelfNodeRemediationTemplate", snrwebhookv1alpha1.SetupSNRTemplateWebhookWithManager},
		{"SelfNodeRemediation", snrwebhookv1alpha1.SetupSNRWebhookWithManager},
	} {
		if err := webhook.setup(mgr); err != nil {
			return fmt.Errorf("register the %s webhook: %w", webhook.kind, err)
		}
	}
	return nil
}

// Initialize prepares all inputs of SNR admission and registers the webhooks.
// Each failure is a startup error: the caller must stop the manager, also when SNR is Disabled.
// The readiness check of the shared listener is not part of this function; see the listener package.
func Initialize(mgr ctrl.Manager) error {
	if mgr == nil {
		return errors.New("SNR admission: the manager is nil")
	}
	// The SelfNodeRemediationConfig validator reads the namespace for each request. Without it, the
	// validator rejects each create and the delete of the default config. See decision S2.
	if _, err := utils.GetDeploymentNamespace(); err != nil {
		return fmt.Errorf("SNR admission: %w", err)
	}
	// The strategy rule uses the Kubernetes version of the cluster, read one time at start. The SNR
	// manager continues when the read fails, and then rejects the OutOfServiceTaint strategy. WAO
	// stops instead, so admission does not answer with an assumed version. See decision S3.
	if err := utils.InitOutOfServiceTaintFlagsWithRetry(context.Background(), mgr.GetConfig()); err != nil {
		return fmt.Errorf("SNR admission: read the Kubernetes version: %w", err)
	}
	return SetupWithManager(mgr)
}
