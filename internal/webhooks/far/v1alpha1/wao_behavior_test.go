package v1alpha1

// WAO file, not copied from FAR. These tests record the FAR admission behavior
// that WAO must preserve. They call the copied defaulters and the imported
// validators with a fake client, so they need no API server.

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonAnnotations "github.com/medik8s/common/pkg/annotations"
	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"
	"github.com/medik8s/fence-agents-remediation/v5/pkg/validation"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	waoTestNamespace  = "far-test"
	waoOtherNamespace = "far-other"
	waoTestNode       = "worker-test"
	waoCustomSecret   = "wao-custom-credentials"
	waoNodeSecret     = "wao-node-credentials"
)

// spyClient records each Secret read. getErr, when set, replaces each Secret read result.
type spyClient struct {
	client.Client
	keys *[]client.ObjectKey
}

func newSpyClient(t *testing.T, getErr error, secrets ...client.Object) spyClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	keys := &[]client.ObjectKey{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secrets...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isSecret := obj.(*corev1.Secret); isSecret {
				*keys = append(*keys, key)
				if getErr != nil {
					return getErr
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	return spyClient{Client: c, keys: keys}
}

// All Secret values in this file are synthetic.
func testSecret(namespace, name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func legacySecret() *corev1.Secret {
	return testSecret(waoTestNamespace, farapi.OldDefaultSecretName, map[string]string{"--username": "synthetic-user"})
}

// validFAR is a local fixture. It must never become a real cluster request.
func validFAR() *farapi.FenceAgentsRemediation {
	return &farapi.FenceAgentsRemediation{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-admission-test", Namespace: waoTestNamespace},
		Spec: farapi.FenceAgentsRemediationSpec{
			Agent:               validAgentName,
			RemediationStrategy: farapi.ResourceDeletionRemediationStrategy,
			NodeParameters: map[farapi.ParameterName]map[farapi.NodeName]string{
				"--ipport": {waoTestNode: "6230"},
			},
		},
	}
}

func validFARTemplate() *farapi.FenceAgentsRemediationTemplate {
	return &farapi.FenceAgentsRemediationTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-admission-test", Namespace: waoTestNamespace},
		Spec: farapi.FenceAgentsRemediationTemplateSpec{
			Template: farapi.FenceAgentsRemediationTemplateResource{Spec: validFAR().Spec},
		},
	}
}

// useTestAdmissionState replaces the FAR package globals for one test and restores the FAR defaults.
func useTestAdmissionState(t *testing.T, outOfServiceTaintSupported bool) {
	t.Helper()
	previous := farapi.IsOutOfServiceTaintSupported
	farapi.SetAgentValidator(validation.NewCustomAgentValidator(func(agent string) (bool, error) {
		return agent == validAgentName, nil
	}))
	farapi.SetOutOfServiceTaintSupported(outOfServiceTaintSupported)
	t.Cleanup(func() {
		farapi.SetAgentValidator(validation.NewAgentValidator())
		farapi.SetOutOfServiceTaintSupported(previous)
	})
}

// defaultKinds runs the copied defaulter of each resource type on a spec with the given Secret reference.
var defaultKinds = map[string]func(ctx context.Context, c client.Client, ref *string, create bool) (*string, error){
	"FenceAgentsRemediation": func(ctx context.Context, c client.Client, ref *string, create bool) (*string, error) {
		far := validFAR()
		far.Spec.SharedSecretName = ref
		if !create {
			far.CreationTimestamp = metav1.Now()
		}
		err := (&farDefaulter{Client: c}).Default(ctx, far)
		return far.Spec.SharedSecretName, err
	},
	"FenceAgentsRemediationTemplate": func(ctx context.Context, c client.Client, ref *string, create bool) (*string, error) {
		tmpl := validFARTemplate()
		tmpl.Spec.Template.Spec.SharedSecretName = ref
		if !create {
			tmpl.CreationTimestamp = metav1.Now()
		}
		err := (&farTemplateDefaulter{Client: c}).Default(ctx, tmpl)
		return tmpl.Spec.Template.Spec.SharedSecretName, err
	},
}

func describeRef(ref *string) string {
	if ref == nil {
		return "nil"
	}
	return `"` + *ref + `"`
}

func sameRef(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func TestWAOSharedSecretDefaulting(t *testing.T) {
	legacy := farapi.OldDefaultSecretName
	type row struct {
		name         string
		operations   []bool // true is CREATE, false is UPDATE
		before       *string
		legacyExists []bool
		want         *string
		wantLookup   bool
	}
	both := []bool{true, false}
	rows := []row{
		{"CREATE, nil, Secret exists", []bool{true}, nil, []bool{true}, &legacy, true},
		{"CREATE, nil, Secret absent", []bool{true}, nil, []bool{false}, nil, true},
		{"UPDATE, nil, Secret exists", []bool{false}, nil, []bool{true}, nil, true},
		{"legacy name, Secret absent", both, &legacy, []bool{false}, nil, true},
		{"legacy name, Secret exists", both, &legacy, []bool{true}, &legacy, true},
		{"custom name", both, ptr.To(waoCustomSecret), both, ptr.To(waoCustomSecret), false},
		{"empty string pointer", both, ptr.To(""), both, ptr.To(""), false},
	}
	for kind, applyDefault := range defaultKinds {
		for _, r := range rows {
			for _, create := range r.operations {
				for _, exists := range r.legacyExists {
					name := kind + "/" + r.name + map[bool]string{true: "/create", false: "/update"}[create] +
						map[bool]string{true: "/exists", false: "/absent"}[exists]
					t.Run(name, func(t *testing.T) {
						var secrets []client.Object
						if exists {
							secrets = append(secrets, legacySecret())
						}
						spy := newSpyClient(t, nil, secrets...)
						var before *string
						if r.before != nil {
							before = ptr.To(*r.before)
						}
						got, err := applyDefault(context.Background(), spy, before, create)
						if err != nil {
							t.Fatal(err)
						}
						if !sameRef(got, r.want) {
							t.Errorf("reference = %s, want %s", describeRef(got), describeRef(r.want))
						}
						if !r.wantLookup && len(*spy.keys) != 0 {
							t.Errorf("unexpected Secret reads: %v", *spy.keys)
						}
						if r.wantLookup && len(*spy.keys) == 0 {
							t.Error("the defaulter did not check the legacy Secret")
						}
						for _, key := range *spy.keys {
							if want := (client.ObjectKey{Namespace: waoTestNamespace, Name: legacy}); key != want {
								t.Errorf("Secret read %v, want %v", key, want)
							}
						}
					})
				}
			}
		}
	}
}

func TestWAOSharedSecretDefaultingReturnsLookupErrors(t *testing.T) {
	legacy := farapi.OldDefaultSecretName
	lookupErrors := map[string]error{
		"forbidden": apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, legacy, errors.New("synthetic denial")),
		"transport": errors.New("synthetic connection refused"),
	}
	for kind, applyDefault := range defaultKinds {
		for errName, lookupErr := range lookupErrors {
			for _, before := range []*string{nil, &legacy} {
				for _, create := range []bool{true, false} {
					name := kind + "/" + errName + "/" + describeRef(before) + map[bool]string{true: "/create", false: "/update"}[create]
					t.Run(name, func(t *testing.T) {
						spy := newSpyClient(t, lookupErr, legacySecret())
						got, err := applyDefault(context.Background(), spy, before, create)
						if err == nil || !strings.Contains(err.Error(), "failed to check for shared secret") {
							t.Fatalf("expected a lookup error, got %v", err)
						}
						if !sameRef(got, before) {
							t.Errorf("reference changed to %s after a failed lookup", describeRef(got))
						}
					})
				}
			}
		}
	}
}

func TestWAOTemplateAnnotationDefaulting(t *testing.T) {
	key := commonAnnotations.MultipleTemplatesSupportedAnnotation
	if key != "remediation.medik8s.io/multiple-templates-support" {
		t.Fatalf("annotation key changed to %q", key)
	}
	for name, tc := range map[string]struct{ before, want map[string]string }{
		"nil annotations":  {nil, map[string]string{key: "true"}},
		"absent key":       {map[string]string{"other": "kept"}, map[string]string{"other": "kept", key: "true"}},
		"existing true":    {map[string]string{key: "true"}, map[string]string{key: "true"}},
		"existing false":   {map[string]string{key: "false"}, map[string]string{key: "false"}},
		"existing unknown": {map[string]string{key: "maybe"}, map[string]string{key: "maybe"}},
	} {
		t.Run(name, func(t *testing.T) {
			tmpl := validFARTemplate()
			tmpl.Annotations = tc.before
			if err := (&farTemplateDefaulter{Client: newSpyClient(t, nil)}).Default(context.Background(), tmpl); err != nil {
				t.Fatal(err)
			}
			if len(tmpl.Annotations) != len(tc.want) {
				t.Fatalf("annotations = %v, want %v", tmpl.Annotations, tc.want)
			}
			for k, v := range tc.want {
				if tmpl.Annotations[k] != v {
					t.Errorf("annotation %s = %q, want %q", k, tmpl.Annotations[k], v)
				}
			}
		})
	}
}

func TestWAOTemplateUpdateLegacySecretRestriction(t *testing.T) {
	useTestAdmissionState(t, true)
	legacy := farapi.OldDefaultSecretName
	oldTmpl := validFARTemplate()
	oldTmpl.Spec.Template.Spec.SharedSecretName = &legacy

	for name, tc := range map[string]struct {
		newRef    *string
		secrets   []client.Object
		lookupErr error
		wantErr   string
	}{
		"reference removed, Secret exists":       {nil, []client.Object{legacySecret()}, nil, "shared secret with the deprecated default name"},
		"reference emptied, Secret exists":       {ptr.To(""), []client.Object{legacySecret()}, nil, "shared secret with the deprecated default name"},
		"reference removed, Secret absent":       {nil, nil, nil, ""},
		"reference removed, lookup fails":        {nil, nil, errors.New("synthetic connection refused"), "failed to check if the default shared secret exists"},
		"reference kept, Secret exists":          {&legacy, []client.Object{legacySecret()}, nil, ""},
		"reference changed to custom and exists": {ptr.To(waoCustomSecret), []client.Object{legacySecret(), testSecret(waoTestNamespace, waoCustomSecret, map[string]string{"--username": "synthetic-user"})}, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			newTmpl := validFARTemplate()
			newTmpl.Spec.Template.Spec.SharedSecretName = tc.newRef
			spy := newSpyClient(t, tc.lookupErr, tc.secrets...)
			_, err := (&farapi.FARTemplateValidator{Client: spy}).ValidateUpdate(context.Background(), oldTmpl, newTmpl)
			checkError(t, err, tc.wantErr)
		})
	}
}

// The legacy Secret restriction applies to templates only.
func TestWAOFARUpdateHasNoLegacySecretRestriction(t *testing.T) {
	useTestAdmissionState(t, true)
	legacy := farapi.OldDefaultSecretName
	oldFAR := validFAR()
	oldFAR.Spec.SharedSecretName = &legacy
	newFAR := validFAR()
	spy := newSpyClient(t, nil, legacySecret())
	if _, err := (&farapi.FARValidator{Client: spy}).ValidateUpdate(context.Background(), oldFAR, newFAR); err != nil {
		t.Fatalf("FAR update was rejected: %v", err)
	}
}

func checkError(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

func TestWAOSecretReadsUseResourceNamespace(t *testing.T) {
	useTestAdmissionState(t, true)
	far := validFAR()
	far.Spec.SharedSecretName = ptr.To(waoCustomSecret)
	far.Spec.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
	// The Secrets in the other namespace have an invalid action. A read from that namespace fails validation.
	spy := newSpyClient(t, nil,
		testSecret(waoTestNamespace, waoCustomSecret, map[string]string{"--username": "synthetic-user"}),
		testSecret(waoTestNamespace, waoNodeSecret, map[string]string{"--password": "synthetic-password"}),
		testSecret(waoOtherNamespace, waoCustomSecret, map[string]string{"--action": "shutdown"}),
		testSecret(waoOtherNamespace, waoNodeSecret, map[string]string{"--action": "shutdown"}),
	)
	if _, err := (&farapi.FARValidator{Client: spy}).ValidateCreate(context.Background(), far); err != nil {
		t.Fatal(err)
	}
	if err := (&farDefaulter{Client: spy}).Default(context.Background(), far); err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, key := range *spy.keys {
		if key.Namespace != waoTestNamespace {
			t.Errorf("Secret read from namespace %q: %v", key.Namespace, key)
		}
		read[key.Name] = true
	}
	if !read[waoCustomSecret] || !read[waoNodeSecret] {
		t.Errorf("expected reads of both Secrets, got %v", *spy.keys)
	}
}

func TestWAOValidationBehavior(t *testing.T) {
	const missingParams = "invalid spec: mandatory parameters are missing"
	const multipleDefinition = "invalid multiple definition of FAR parameter"
	sharedSecret := func(data map[string]string) client.Object {
		return testSecret(waoTestNamespace, waoCustomSecret, data)
	}
	nodeSecret := func(data map[string]string) client.Object {
		return testSecret(waoTestNamespace, waoNodeSecret, data)
	}
	type testCase struct {
		name             string
		taintUnsupported bool
		mutate           func(spec *farapi.FenceAgentsRemediationSpec)
		secrets          []client.Object
		wantErr          string
	}
	cases := []testCase{
		{name: "valid base object"},
		{name: "unsupported agent", mutate: func(s *farapi.FenceAgentsRemediationSpec) { s.Agent = "fence_wao_missing" },
			wantErr: "unsupported fence agent: fence_wao_missing"},
		{name: "OutOfServiceTaint on a supported cluster",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.RemediationStrategy = farapi.OutOfServiceTaintRemediationStrategy
			}},
		{name: "OutOfServiceTaint on an unsupported cluster", taintUnsupported: true,
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.RemediationStrategy = farapi.OutOfServiceTaintRemediationStrategy
			},
			wantErr: outOfServiceTaintUnsupportedMsg},
		{name: "ResourceDeletion on an unsupported cluster", taintUnsupported: true},
		{name: "malformed shared parameter template",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.SharedParameters = map[farapi.ParameterName]string{"--plug": "{{.NodeName"}
			},
			wantErr: "invalid NodeTemplate syntax in shared parameter --plug"},
		{name: "no parameters and no Secrets",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) { s.NodeParameters = nil }, wantErr: missingParams},
		{name: "node parameter without a node value",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeParameters = map[farapi.ParameterName]map[farapi.NodeName]string{"--ipport": {}}
			}, wantErr: missingParams},
		// The current shared-only condition: shared values without a node template are not sufficient.
		{name: "shared parameters only, no template",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeParameters = nil
				s.SharedParameters = map[farapi.ParameterName]string{"--ip": "192.0.2.10"}
			}, wantErr: missingParams},
		{name: "shared parameters only, with NodeName template",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeParameters = nil
				s.SharedParameters = map[farapi.ParameterName]string{"--plug": "vm-{{.NodeName}}"}
			}},
		{name: "shared Secret only, no template",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeParameters = nil
				s.SharedSecretName = ptr.To(waoCustomSecret)
			}, secrets: []client.Object{sharedSecret(map[string]string{"--username": "synthetic-user"})}, wantErr: missingParams},
		{name: "shared Secret only, with NodeName template",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeParameters = nil
				s.SharedSecretName = ptr.To(waoCustomSecret)
			}, secrets: []client.Object{sharedSecret(map[string]string{"--plug": "vm-{{.NodeName}}"})}},
		{name: "malformed template in the shared Secret",
			mutate:  func(s *farapi.FenceAgentsRemediationSpec) { s.SharedSecretName = ptr.To(waoCustomSecret) },
			secrets: []client.Object{sharedSecret(map[string]string{"--plug": "{{.NodeName"})}, wantErr: "template"},
		{name: "shared Secret is missing",
			mutate:  func(s *farapi.FenceAgentsRemediationSpec) { s.SharedSecretName = ptr.To(waoCustomSecret) },
			wantErr: "secret '" + waoCustomSecret + "' not found in namespace '" + waoTestNamespace + "'"},
		{name: "node Secret is missing",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
			}, wantErr: "secret '" + waoNodeSecret + "' not found in namespace '" + waoTestNamespace + "'"},
		{name: "shared Secret and node Secret parameters",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.SharedSecretName = ptr.To(waoCustomSecret)
				s.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
			}, secrets: []client.Object{sharedSecret(map[string]string{"--username": "synthetic-user"}), nodeSecret(map[string]string{"--password": "synthetic-password"})}},
		{name: "node Secret overrides shared Secret",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.SharedSecretName = ptr.To(waoCustomSecret)
				s.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
			}, secrets: []client.Object{sharedSecret(map[string]string{"--password": "synthetic-shared"}), nodeSecret(map[string]string{"--password": "synthetic-node"})}},
		{name: "node parameter overrides shared parameter",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.SharedParameters = map[farapi.ParameterName]string{"--ipport": "1"}
			}},
		{name: "shared parameter collides with shared Secret",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.SharedParameters = map[farapi.ParameterName]string{"--username": "admin"}
				s.SharedSecretName = ptr.To(waoCustomSecret)
			}, secrets: []client.Object{sharedSecret(map[string]string{"--username": "synthetic-user"})}, wantErr: multipleDefinition},
		{name: "node parameter collides with node Secret",
			mutate: func(s *farapi.FenceAgentsRemediationSpec) {
				s.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
			}, secrets: []client.Object{nodeSecret(map[string]string{"--ipport": "6231"})}, wantErr: multipleDefinition},
	}
	for _, actionName := range []string{farapi.ActionName, farapi.ParameterActionName} {
		for value, wantErr := range map[string]string{"": "", "reboot": "", "off": "", "shutdown": farapi.ErrorUnsupportedAction, "REBOOT": farapi.ErrorUnsupportedAction} {
			label := actionName + "=" + `"` + value + `"`
			cases = append(cases,
				testCase{name: "shared parameter " + label, wantErr: wantErr, mutate: func(s *farapi.FenceAgentsRemediationSpec) {
					s.SharedParameters = map[farapi.ParameterName]string{farapi.ParameterName(actionName): value}
				}},
				testCase{name: "node parameter " + label, wantErr: wantErr, mutate: func(s *farapi.FenceAgentsRemediationSpec) {
					s.NodeParameters[farapi.ParameterName(actionName)] = map[farapi.NodeName]string{waoTestNode: value}
				}},
				testCase{name: "shared Secret " + label, wantErr: wantErr, secrets: []client.Object{sharedSecret(map[string]string{actionName: value})},
					mutate: func(s *farapi.FenceAgentsRemediationSpec) { s.SharedSecretName = ptr.To(waoCustomSecret) }},
				testCase{name: "node Secret " + label, wantErr: wantErr, secrets: []client.Object{nodeSecret(map[string]string{actionName: value})},
					mutate: func(s *farapi.FenceAgentsRemediationSpec) {
						s.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
					}},
			)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTestAdmissionState(t, !tc.taintUnsupported)
			far, tmpl := validFAR(), validFARTemplate()
			if tc.mutate != nil {
				tc.mutate(&far.Spec)
				tc.mutate(&tmpl.Spec.Template.Spec)
			}
			spy := newSpyClient(t, nil, tc.secrets...)
			ctx := context.Background()
			_, err := (&farapi.FARValidator{Client: spy}).ValidateCreate(ctx, far)
			checkError(t, err, tc.wantErr)
			_, err = (&farapi.FARValidator{Client: spy}).ValidateUpdate(ctx, validFAR(), far)
			checkError(t, err, tc.wantErr)
			_, err = (&farapi.FARTemplateValidator{Client: spy}).ValidateCreate(ctx, tmpl)
			checkError(t, err, tc.wantErr)
			_, err = (&farapi.FARTemplateValidator{Client: spy}).ValidateUpdate(ctx, validFARTemplate(), tmpl)
			checkError(t, err, tc.wantErr)
		})
	}
}

func TestWAOParameterRenderingAndPrecedence(t *testing.T) {
	far := validFAR()
	far.Name = waoTestNode
	far.Spec.SharedParameters = map[farapi.ParameterName]string{"--plug": "vm-{{.NodeName}}", "--ipport": "1"}
	far.Spec.SharedSecretName = ptr.To(waoCustomSecret)
	far.Spec.NodeSecretNames = map[farapi.NodeName]string{waoTestNode: waoNodeSecret}
	spy := newSpyClient(t, nil,
		testSecret(waoTestNamespace, waoCustomSecret, map[string]string{"--password": "synthetic-shared", "--username": "user-{{.NodeName}}"}),
		testSecret(waoTestNamespace, waoNodeSecret, map[string]string{"--password": "synthetic-node"}),
	)
	params, _, err := farapi.BuildFenceAgentParams(context.Background(), spy, far)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[farapi.ParameterName]string{
		"--plug":     "vm-" + waoTestNode,   // shared parameter rendered with the node name
		"--ipport":   "6230",                // node parameter overrides the shared parameter
		"--username": "user-" + waoTestNode, // shared Secret value rendered with the node name
		"--password": "synthetic-node",      // node Secret overrides the shared Secret
	} {
		if params[name] != want {
			t.Errorf("parameter %s = %q, want %q", name, params[name], want)
		}
	}
}
