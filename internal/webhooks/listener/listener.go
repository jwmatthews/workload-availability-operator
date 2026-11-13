// Package listener holds what the admission webhooks of all components share in
// the WAO manager: the webhook listener readiness and the dedicated client.
// One listener in each WAO replica serves the webhooks of all components.
package listener

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadyCheckName is the name of the readiness check of the local webhook listener.
const ReadyCheckName = "admission"

// NewAdmissionClient returns a dedicated client for admission. The client reads
// directly from the API server. Thus WAO does not list, watch, or cache the
// objects that admission reads, and each admission decision uses the current object.
func NewAdmissionClient(cfg *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	if cfg == nil || scheme == nil {
		return nil, errors.New("admission client: the REST config and the scheme are mandatory")
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("admission client: %w", err)
	}
	return c, nil
}

// AddReadyCheck adds the readiness check of the local webhook listener. Call it one time.
// The check uses only the local listener. It does not use an operand Deployment or the WAO Service endpoints.
func AddReadyCheck(mgr ctrl.Manager) error {
	if mgr == nil {
		return errors.New("admission: the manager is nil")
	}
	if err := mgr.AddReadyzCheck(ReadyCheckName, mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("admission: add the readiness check: %w", err)
	}
	return nil
}

// Ready returns a function that reports if the local webhook listener of this replica serves.
func Ready(mgr ctrl.Manager) func() bool {
	check := mgr.GetWebhookServer().StartedChecker()
	return func() bool { return check(nil) == nil }
}
