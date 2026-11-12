package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/medik8s/workload-availability-operator/internal/images"
)

// These tests start the built manager binary against an envtest API server. They prove the
// wiring in main: the inventory flag, the certificate path, FAR admission initialization
// and the readiness check. A removed Initialize call makes them fail.

const (
	testNamespace = "wao-main-test"
	validatePath  = "/validate-fence-agents-remediation-medik8s-io-v1alpha1-fenceagentsremediation"
)

type mainEnv struct {
	binary     string
	kubeconfig string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeServingCert writes a synthetic self-signed serving certificate as tls.crt and tls.key.
func writeServingCert(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "wao-main-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	writeFile(t, filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func startMainEnv(t *testing.T) *mainEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run this suite through make test or make test-envtest")
	}
	root := filepath.Join("..")
	operandKeys := []string{"nhc", "snr", "sbr", "far", "mdr", "nmo"}
	crdPaths := make([]string, 0, len(operandKeys)+1)
	crdPaths = append(crdPaths, filepath.Join(root, "config", "crd", "bases"))
	for _, key := range operandKeys {
		crdPaths = append(crdPaths, filepath.Join(root, "config", "operands", key, "config", "crd", "bases"))
	}
	testEnv := &envtest.Environment{CRDDirectoryPaths: crdPaths, ErrorIfCRDPathMissing: true}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	apiClient, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	if err := apiClient.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	user, err := testEnv.AddUser(envtest.User{Name: "wao-main-test", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	kubeconfig, err := user.KubeConfig()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := &mainEnv{binary: filepath.Join(dir, "manager"), kubeconfig: filepath.Join(dir, "kubeconfig")}
	writeFile(t, env.kubeconfig, kubeconfig)
	if out, err := exec.Command("go", "build", "-o", env.binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build the manager: %v\n%s", err, out)
	}
	return env
}

type managerProcess struct {
	cmd    *exec.Cmd
	output *lockedBuffer
	exited chan error
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs the manager binary with the production flags of config/manager/manager.yaml,
// except leader election and the ports.
func (e *mainEnv) start(t *testing.T, args ...string) *managerProcess {
	t.Helper()
	cmd := exec.Command(e.binary, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+e.kubeconfig, "OPERATOR_NAMESPACE="+testNamespace)
	for _, name := range images.Required {
		cmd.Env = append(cmd.Env, name+"=example.test/operand:test")
	}
	p := &managerProcess{cmd: cmd, output: &lockedBuffer{}, exited: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = p.output, p.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
		}
	})
	return p
}

func TestManagerStopsWithoutAgentInventory(t *testing.T) {
	env := startMainEnv(t)
	certDir := t.TempDir()
	writeServingCert(t, certDir)
	p := env.start(t,
		"--health-probe-bind-address=127.0.0.1:0",
		"--far-agent-inventory="+filepath.Join(t.TempDir(), "agents.txt"),
		"--webhook-cert-path="+certDir,
		fmt.Sprintf("--webhook-port=%d", freePort(t)),
	)
	select {
	case err := <-p.exited:
		if err == nil {
			t.Fatalf("the manager stopped without an error:\n%s", p.output)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("the manager continued without an agent inventory:\n%s", p.output)
	}
	if !strings.Contains(p.output.String(), "unable to initialize FAR admission") {
		t.Errorf("the output does not name FAR admission:\n%s", p.output)
	}
}

func TestManagerServesFARAdmission(t *testing.T) {
	env := startMainEnv(t)
	certDir := t.TempDir()
	writeServingCert(t, certDir)
	inventory := filepath.Join(t.TempDir(), "agents.txt")
	writeFile(t, inventory, []byte("fence_wao_listed\n"))
	probePort, webhookPort := freePort(t), freePort(t)
	p := env.start(t,
		fmt.Sprintf("--health-probe-bind-address=127.0.0.1:%d", probePort),
		"--far-agent-inventory="+inventory,
		"--webhook-cert-path="+certDir,
		fmt.Sprintf("--webhook-port=%d", webhookPort),
	)

	httpClient := &http.Client{Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-p.exited:
			t.Fatalf("the manager stopped: %v\n%s", err, p.output)
		default:
		}
		resp, err := httpClient.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", probePort))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the manager did not become ready:\n%s", p.output)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The registered validator must answer, and it must use the inventory from the flag.
	for agent, wantAllowed := range map[string]bool{"fence_wao_listed": true, "fence_ipmilan": false} {
		review := fmt.Sprintf(`{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview","request":{"uid":"wao-main-test",
"kind":{"group":"fence-agents-remediation.medik8s.io","version":"v1alpha1","kind":"FenceAgentsRemediation"},
"operation":"CREATE","namespace":%q,"object":{"apiVersion":"fence-agents-remediation.medik8s.io/v1alpha1",
"kind":"FenceAgentsRemediation","metadata":{"name":"wao-main-test","namespace":%q},
"spec":{"agent":%q,"remediationStrategy":"ResourceDeletion","nodeparameters":{"--ipport":{"worker-test":"6230"}}}}}}`,
			testNamespace, testNamespace, agent)
		resp, err := httpClient.Post(fmt.Sprintf("https://127.0.0.1:%d%s", webhookPort, validatePath),
			"application/json", strings.NewReader(review))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var out struct {
			Response struct {
				Allowed bool `json:"allowed"`
				Status  struct {
					Message string `json:"message"`
				} `json:"status"`
			} `json:"response"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("agent %s: HTTP %d: %s", agent, resp.StatusCode, body)
		}
		if out.Response.Allowed != wantAllowed {
			t.Errorf("agent %s: allowed = %v, want %v (%s)",
				agent, out.Response.Allowed, wantAllowed, out.Response.Status.Message)
		}
		if !wantAllowed && !strings.Contains(out.Response.Status.Message, "unsupported fence agent: "+agent) {
			t.Errorf("agent %s: message %q", agent, out.Response.Status.Message)
		}
	}
}
