package far

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	listedAgent      = "fence_ipmilan"
	leaderElectionID = "wao-far-admission-test"
)

// admissionEnv is an envtest API server with the four FAR registrations routed to WAO managers
// in this process. No FAR controller and no fence agent executor run here.
type admissionEnv struct {
	cfg       *rest.Config
	client    client.Client // a plain API user; requests go through admission
	inventory string
	options   envtest.WebhookInstallOptions
}

type replica struct {
	mgr      ctrl.Manager
	addr     string
	done     chan error
	stop     context.CancelFunc
	stopOnce sync.Once
}

func repoRoot() string { return filepath.Join("..", "..", "..") }

// startAdmissionEnv starts the API server with the WAO CRD, the six operand CRD sets and the
// registrations that controller-gen writes from the markers of the copied FAR files.
func startAdmissionEnv(t *testing.T) *admissionEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run this suite through make test or make test-envtest")
	}
	restoreFARState(t)
	operandKeys := []string{"nhc", "snr", "sbr", "far", "mdr", "nmo"}
	crdPaths := make([]string, 0, len(operandKeys)+1)
	crdPaths = append(crdPaths, filepath.Join(repoRoot(), "config", "crd", "bases"))
	for _, key := range operandKeys {
		crdPaths = append(crdPaths, filepath.Join(repoRoot(), "config", "operands", key, "config", "crd", "bases"))
	}
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join(repoRoot(), "config", "webhook", "manifests.yaml")},
		},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	apiClient, err := client.New(cfg, client.Options{Scheme: farScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	env := &admissionEnv{cfg: cfg, client: apiClient, options: testEnv.WebhookInstallOptions,
		inventory: writeInventory(t, listedAgent+"\nfence_wao_listed\n")}
	for _, name := range []string{testNamespace, "far-other"} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := apiClient.Create(context.Background(), ns); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

// startReplica starts a WAO manager the way cmd/main.go does for FAR admission: leader election
// for reconciliation, and Initialize with the checked inventory and the dedicated uncached client.
func (e *admissionEnv) startReplica(t *testing.T, port int) *replica {
	t.Helper()
	mgr, err := ctrl.NewManager(e.cfg, ctrl.Options{
		Scheme:                  farScheme(t),
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  "0",
		LeaderElection:          true,
		LeaderElectionID:        leaderElectionID,
		LeaderElectionNamespace: "default",
		WebhookServer: webhook.NewServer(webhook.Options{
			Host: e.options.LocalServingHost, Port: port, CertDir: e.options.LocalServingCertDir,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(mgr, e.inventory); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &replica{mgr: mgr, addr: fmt.Sprintf("%s:%d", e.options.LocalServingHost, port), done: make(chan error, 1), stop: cancel}
	go func() { r.done <- mgr.Start(ctx) }()
	t.Cleanup(func() { r.shutdown(t) })
	eventually(t, "the webhook listener of the replica serves", Ready(mgr))
	return r
}

// shutdown stops the manager and waits for it. A second call does nothing.
func (r *replica) shutdown(t *testing.T) {
	t.Helper()
	r.stopOnce.Do(func() {
		r.stop()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("manager stop: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop")
		}
	})
}

func (r *replica) elected() bool {
	select {
	case <-r.mgr.Elected():
		return true
	default:
		return false
	}
}

// reviewHTTPS sends a CREATE AdmissionReview directly to the webhook listener of one replica.
func reviewHTTPS(t *testing.T, addr, path, kind string, obj any) *admissionv1.AdmissionResponse {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       "wao-test",
			Kind:      metav1.GroupVersionKind{Group: farapi.GroupVersion.Group, Version: farapi.GroupVersion.Version, Kind: kind},
			Operation: admissionv1.Create,
			Namespace: testNamespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test certificate of envtest has no name that this direct request can verify.
	httpClient := &http.Client{Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	resp, err := httpClient.Post("https://"+addr+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out admissionv1.AdmissionReview
	if err := json.Unmarshal(data, &out); err != nil || out.Response == nil {
		t.Fatalf("%s: HTTP %d, body %s, error %v", path, resp.StatusCode, data, err)
	}
	return out.Response
}
