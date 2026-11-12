package far

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewAdmissionClient returns the dedicated client of FAR admission. The client
// reads directly from the API server. Thus WAO does not list, watch, or cache
// Secrets, and each admission decision uses the current Secret.
func NewAdmissionClient(cfg *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	if cfg == nil || scheme == nil {
		return nil, errors.New("FAR admission client: the REST config and the scheme are mandatory")
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("FAR admission client: %w", err)
	}
	return c, nil
}
