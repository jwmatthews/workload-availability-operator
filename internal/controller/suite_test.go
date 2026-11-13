package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/images"
)

var (
	k8sClient client.Client
	testEnv   *envtest.Environment
	cancel    context.CancelFunc
)

func testImages() images.Set {
	s := images.Set{}
	for _, k := range images.Required {
		s[k] = "example.test/" + strings.ToLower(k) + "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}
	return s
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	SetDefaultEventuallyTimeout(20 * time.Second)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	// The WAO CRD plus the operand CRDs, which live in the copied trees (config/operands/crd).
	operandKeys := []string{"nhc", "snr", "sbr", "far", "mdr", "nmo"}
	crdPaths := make([]string, 0, len(operandKeys)+1)
	crdPaths = append(crdPaths, filepath.Join("..", "..", "config", "crd", "bases"))
	for _, k := range operandKeys {
		crdPaths = append(crdPaths, filepath.Join("..", "..", "config", "operands", k, "config", "crd", "bases"))
	}
	testEnv = &envtest.Environment{CRDDirectoryPaths: crdPaths, ErrorIfCRDPathMissing: true}
	// Allow running from an IDE without KUBEBUILDER_ASSETS (run 'make setup-envtest' first).
	// An explicit KUBEBUILDER_ASSETS always selects the assets.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
			testEnv.BinaryAssetsDirectory = dir
		}
	}
	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(v1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	Expect(err).NotTo(HaveOccurred())
	Expect((&WorkloadAvailabilityConfigReconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Namespace: ns, Images: testImages(),
		CentralAdmissionReady: listenerReady.Load,
	}).SetupWithManager(mgr)).To(Succeed())
	go func() { defer GinkgoRecover(); Expect(mgr.Start(ctx)).To(Succeed()) }()
})

// listenerReady is the test value of the central admission readiness callback.
var listenerReady atomic.Bool

var _ = AfterSuite(func() {
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// getFirstFoundEnvTestBinaryDir returns the first envtest binary directory under bin/k8s, or "".
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
