package far

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/medik8s/workload-availability-operator/internal/webhooks/listener"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
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
		Subject:      pkix.Name{CommonName: "wao-test"},
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
	for name, block := range map[string]*pem.Block{
		"tls.crt": {Type: "CERTIFICATE", Bytes: der},
		"tls.key": {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type servingManager struct {
	mgr      ctrl.Manager
	probe    string
	startErr chan error
	stop     context.CancelFunc
}

// startServingManager initializes FAR admission and starts a manager against the stub API server.
func startServingManager(t *testing.T, cfg *rest.Config, withCerts bool) *servingManager {
	t.Helper()
	restoreFARState(t)
	certDir := t.TempDir()
	if withCerts {
		writeServingCert(t, certDir)
	}
	probe := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 farScheme(t),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: probe,
		WebhookServer:          webhook.NewServer(webhook.Options{Host: "127.0.0.1", Port: freePort(t), CertDir: certDir}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(mgr, writeInventory(t, testAgent+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := listener.AddReadyCheck(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &servingManager{mgr: mgr, probe: probe, startErr: make(chan error, 1), stop: cancel}
	go func() { s.startErr <- mgr.Start(ctx) }()
	t.Cleanup(cancel)
	return s
}

func probeStatus(addr, path string) (int, error) {
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestReadinessFollowsTheLocalWebhookListener(t *testing.T) {
	s := startServingManager(t, versionServer(t, "29", http.StatusOK), true)

	eventually(t, "the admission readiness check passes", func() bool {
		code, err := probeStatus(s.probe, "/readyz/"+listener.ReadyCheckName)
		return err == nil && code == http.StatusOK
	})
	if code, err := probeStatus(s.probe, "/readyz"); err != nil || code != http.StatusOK {
		t.Errorf("/readyz = %d, %v, want 200", code, err)
	}
	if !listener.Ready(s.mgr)() {
		t.Error("Ready reports false while the listener serves")
	}

	// A listener that does not serve must make the check fail.
	s.stop()
	if err := <-s.startErr; err != nil {
		t.Fatalf("manager stop: %v", err)
	}
	if listener.Ready(s.mgr)() {
		t.Error("Ready reports true after the listener stopped")
	}
}

func TestNotReadyBeforeTheListenerStarts(t *testing.T) {
	restoreFARState(t)
	mgr := newTestManager(t, versionServer(t, "29", http.StatusOK), farScheme(t))
	if err := Initialize(mgr, writeInventory(t, testAgent+"\n")); err != nil {
		t.Fatal(err)
	}
	if listener.Ready(mgr)() {
		t.Error("Ready reports true before the manager starts")
	}
}

func TestMissingCertificatesPreventReadiness(t *testing.T) {
	s := startServingManager(t, versionServer(t, "29", http.StatusOK), false)
	select {
	case err := <-s.startErr:
		if err == nil {
			t.Fatal("the manager stopped without an error although the certificates are missing")
		}
	case <-time.After(15 * time.Second):
		// The server waits for certificates. It must not report ready meanwhile.
	}
	if listener.Ready(s.mgr)() {
		t.Error("Ready reports true without serving certificates")
	}
	if code, err := probeStatus(s.probe, "/readyz"); err == nil && code == http.StatusOK {
		t.Error("/readyz is 200 without serving certificates")
	}
}
