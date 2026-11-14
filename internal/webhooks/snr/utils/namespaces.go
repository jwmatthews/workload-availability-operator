package utils

import (
	"fmt"
	"os"
)

// GetDeploymentNamespace returns the Namespace this operator is deployed on.
func GetDeploymentNamespace() (string, error) {
	// deployNamespaceEnvVar is the constant for env variable DEPLOYMENT_NAMESPACE
	// which specifies the Namespace to watch.
	// An empty value means the operator is running with cluster scope.
	// WAO adaptation: the WAO Deployment sets OPERATOR_NAMESPACE, not DEPLOYMENT_NAMESPACE.
	// WAO installs each operand in its own namespace, so the value is the same.
	var deployNamespaceEnvVar = "OPERATOR_NAMESPACE"

	ns, found := os.LookupEnv(deployNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", deployNamespaceEnvVar)
	}
	return ns, nil
}
