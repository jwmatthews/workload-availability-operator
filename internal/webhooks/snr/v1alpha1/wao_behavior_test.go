package v1alpha1

// WAO file, not copied from SNR. These tests record the SNR admission behavior
// that WAO must preserve. They call the copied defaulter and validators directly,
// so they need no API server. See docs/snr-webhooks.md.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	commonAnnotations "github.com/medik8s/common/pkg/annotations"
	remediationv1alpha1 "github.com/medik8s/self-node-remediation/v5/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/snr/utils"
)

const (
	// waoNamespaceEnvVar is the variable that the WAO Deployment sets (decision S2).
	waoNamespaceEnvVar = "OPERATOR_NAMESPACE"
	waoNamespace       = "wao-namespace"
)

// waoSetNamespace sets or removes the namespace variable for one test.
func waoSetNamespace(t *testing.T, value string, set bool) {
	t.Helper()
	t.Setenv(waoNamespaceEnvVar, value)
	if !set {
		if err := os.Unsetenv(waoNamespaceEnvVar); err != nil {
			t.Fatal(err)
		}
	}
}

// waoSetTaintSupport sets the package variable of the strategy rule for one test.
func waoSetTaintSupport(t *testing.T, supported bool) {
	t.Helper()
	before := utils.IsOutOfServiceTaintSupported
	t.Cleanup(func() { utils.IsOutOfServiceTaintSupported = before })
	utils.IsOutOfServiceTaintSupported = supported
}

// waoConfig returns a config with the durations that the CRD defaults give. The API server applies
// the defaults before it calls the validator, and the validator reads each duration without a nil check.
func waoConfig(name, namespace string) *remediationv1alpha1.SelfNodeRemediationConfig {
	duration := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }
	config := remediationv1alpha1.NewDefaultSelfNodeRemediationConfig()
	config.Name, config.Namespace = name, namespace
	config.Spec.PeerApiServerTimeout = duration(5 * time.Second)
	config.Spec.ApiServerTimeout = duration(5 * time.Second)
	config.Spec.PeerDialTimeout = duration(5 * time.Second)
	config.Spec.PeerRequestTimeout = duration(7 * time.Second)
	config.Spec.ApiCheckInterval = duration(15 * time.Second)
	config.Spec.PeerUpdateInterval = duration(15 * time.Minute)
	return &config
}

func waoWantError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Errorf("error = %v, want an error that contains %q", err, text)
	}
}

func waoWantPass(t *testing.T, warnings admission.Warnings, err error) {
	t.Helper()
	if err != nil || len(warnings) != 0 {
		t.Errorf("warnings %v, error %v, want a pass with no warning", warnings, err)
	}
}

// The singleton rule permits one config: the default name in the namespace of the WAO installation.
// The SNR manager creates exactly that object at start, so the rule must accept it.
func TestWAOConfigSingletonRule(t *testing.T) {
	waoSetNamespace(t, waoNamespace, true)
	validator := &SNRConfigValidator{}
	ctx := context.Background()

	warnings, err := validator.ValidateCreate(ctx, waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace))
	waoWantPass(t, warnings, err)

	_, err = validator.ValidateCreate(ctx, waoConfig("other-name", waoNamespace))
	waoWantError(t, err, "a name other than self-node-remediation-config is not allowed")

	_, err = validator.ValidateCreate(ctx, waoConfig(remediationv1alpha1.ConfigCRName, "other-namespace"))
	waoWantError(t, err, "SelfNodeRemediationConfig is only allowed to be created in the namespace: "+waoNamespace)

	// The rule is for CREATE only. An object that exists with a different name can change.
	old := waoConfig("other-name", "other-namespace")
	warnings, err = validator.ValidateUpdate(ctx, old, old.DeepCopy())
	waoWantPass(t, warnings, err)
}

// Without the namespace variable the validator cannot answer. It rejects each create and the delete
// of an object with the default name. Thus the WAO manager stops at start when the variable is
// absent (internal/webhooks/snr/setup.go). The error text names the WAO variable.
func TestWAOConfigRulesWithoutTheNamespace(t *testing.T) {
	waoSetNamespace(t, "", false)
	validator := &SNRConfigValidator{}
	ctx := context.Background()

	_, err := validator.ValidateCreate(ctx, waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace))
	waoWantError(t, err, "failed to verify the deployment namespace SelfNodeRemediationConfig can not be created")

	_, err = validator.ValidateDelete(ctx, waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace))
	waoWantError(t, err, waoNamespaceEnvVar+" must be set")

	// The delete rule reads the namespace only for the default name.
	warnings, err := validator.ValidateDelete(ctx, waoConfig("other-name", waoNamespace))
	waoWantPass(t, warnings, err)

	// The update rules do not read the namespace.
	old := waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace)
	warnings, err = validator.ValidateUpdate(ctx, old, old.DeepCopy())
	waoWantPass(t, warnings, err)
}

// No rule rejects a delete when the namespace is known. A person must be able to delete the config
// to remove the agents, also while SNR is Disabled.
func TestWAOConfigDeleteGivesOnlyAWarning(t *testing.T) {
	waoSetNamespace(t, waoNamespace, true)
	validator := &SNRConfigValidator{}
	ctx := context.Background()

	warnings, err := validator.ValidateDelete(ctx, waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace))
	if err != nil || len(warnings) != 1 ||
		warnings[0] != "The default configuration is deleted, Self Node Remediation is now disabled" {
		t.Errorf("warnings %v, error %v, want a pass with the SNR warning", warnings, err)
	}
	for _, config := range []*remediationv1alpha1.SelfNodeRemediationConfig{
		waoConfig(remediationv1alpha1.ConfigCRName, "other-namespace"),
		waoConfig("other-name", waoNamespace),
	} {
		warnings, err := validator.ValidateDelete(ctx, config)
		waoWantPass(t, warnings, err)
	}
}

// A timeout pair that is not safe gives a warning, not a rejection.
func TestWAOConfigPeerTimeoutGivesOnlyAWarning(t *testing.T) {
	waoSetNamespace(t, waoNamespace, true)
	config := waoConfig(remediationv1alpha1.ConfigCRName, waoNamespace)
	config.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 6 * time.Second}
	warnings, err := (&SNRConfigValidator{}).ValidateCreate(context.Background(), config)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "PeerRequestTimeout (6s) is less than ApiServerTimeout + MinimumBuffer") {
		t.Errorf("warnings %v, error %v, want a pass with the timeout warning", warnings, err)
	}
}

// The defaulter adds the annotation only when the key is absent. It does not replace a value.
func TestWAOTemplateDefaulterAddsTheAnnotationOnlyWhenAbsent(t *testing.T) {
	const key = commonAnnotations.MultipleTemplatesSupportedAnnotation
	for name, tc := range map[string]struct {
		before, want map[string]string
	}{
		"no annotations":   {nil, map[string]string{key: "true"}},
		"other annotation": {map[string]string{"wao.test/other": "kept"}, map[string]string{"wao.test/other": "kept", key: "true"}},
		"value false":      {map[string]string{key: "false"}, map[string]string{key: "false"}},
		"empty value":      {map[string]string{key: ""}, map[string]string{key: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			template := &remediationv1alpha1.SelfNodeRemediationTemplate{}
			template.Annotations = tc.before
			if err := (&SNRTemplateDefaulter{}).Default(context.Background(), template); err != nil {
				t.Fatal(err)
			}
			got := template.GetAnnotations()
			if len(got) != len(tc.want) {
				t.Fatalf("annotations = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if value, found := got[k]; !found || value != v {
					t.Errorf("annotations = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The strategy rule rejects only OutOfServiceTaint, and only when the cluster does not support it.
// On each Kubernetes version that the WAO CSV permits, the rule rejects nothing.
func TestWAOStrategyRule(t *testing.T) {
	ctx := context.Background()
	const rejection = "OutOfServiceTaint remediation strategy is not supported at kubernetes version lower than 1.26"
	strategies := []remediationv1alpha1.RemediationStrategyType{
		remediationv1alpha1.AutomaticRemediationStrategy,
		remediationv1alpha1.ResourceDeletionRemediationStrategy,
		remediationv1alpha1.OutOfServiceTaintRemediationStrategy,
		"",
	}
	for _, supported := range []bool{true, false} {
		for _, strategy := range strategies {
			name := string(strategy)
			if !supported {
				name += " without support"
			}
			t.Run(name, func(t *testing.T) {
				waoSetTaintSupport(t, supported)
				template := &remediationv1alpha1.SelfNodeRemediationTemplate{}
				template.Spec.Template.Spec.RemediationStrategy = strategy
				remediation := &remediationv1alpha1.SelfNodeRemediation{}
				remediation.Spec.RemediationStrategy = strategy

				_, templateCreate := (&SNRTemplateValidator{}).ValidateCreate(ctx, template)
				_, templateUpdate := (&SNRTemplateValidator{}).ValidateUpdate(ctx, template, template)
				_, remediationCreate := (&SNRValidator{}).ValidateCreate(ctx, remediation)
				_, remediationUpdate := (&SNRValidator{}).ValidateUpdate(ctx, remediation, remediation)
				wantRejection := !supported && strategy == remediationv1alpha1.OutOfServiceTaintRemediationStrategy
				for operation, err := range map[string]error{"template create": templateCreate, "template update": templateUpdate,
					"remediation create": remediationCreate, "remediation update": remediationUpdate} {
					if wantRejection {
						waoWantError(t, err, rejection)
					} else if err != nil {
						t.Errorf("%s: error = %v, want a pass", operation, err)
					}
				}
				// The registrations have no DELETE for these kinds, and the functions reject nothing.
				if _, err := (&SNRTemplateValidator{}).ValidateDelete(ctx, template); err != nil {
					t.Errorf("template delete: %v", err)
				}
				if _, err := (&SNRValidator{}).ValidateDelete(ctx, remediation); err != nil {
					t.Errorf("remediation delete: %v", err)
				}
			})
		}
	}
}
