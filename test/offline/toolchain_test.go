package offline

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func firstMatch(t *testing.T, text, pattern, what string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("%s: no match for %s", what, pattern)
	}
	return m[1]
}

func TestLinterVersionMatchesCI(t *testing.T) {
	local := firstMatch(t, readRepoFile(t, "Makefile"), `(?m)^GOLANGCI_LINT_VERSION = (\S+)$`, "Makefile")
	ci := firstMatch(t, readRepoFile(t, ".github/workflows/lint.yml"), `(?m)^\s+version: (\S+)$`, "lint.yml")
	if local != ci {
		t.Errorf("golangci-lint: Makefile %s, CI %s", local, ci)
	}
}

// The admission suites of all components must be part of the normal test target, and CI must run the
// envtest suites on the minimum Kubernetes version and on the default version.
func TestEnvtestSuitesRunLocallyAndInCI(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	packages := firstMatch(t, makefile, `(?m)^ENVTEST_PACKAGES := (.*)$`, "ENVTEST_PACKAGES")
	for _, pkg := range []string{"./internal/controller/...", "./internal/webhooks/..."} {
		if !strings.Contains(packages, pkg) {
			t.Errorf("ENVTEST_PACKAGES %q does not contain %s", packages, pkg)
		}
	}
	for _, target := range []string{"test-no-verify-changes", "test-envtest"} {
		recipe := firstMatch(t, makefile, `(?m)^`+target+`:.*\n((?:\t.*\n)+)`, target)
		if !strings.Contains(recipe, "go test $(ENVTEST_PACKAGES)") {
			t.Errorf("%s does not test $(ENVTEST_PACKAGES): %s", target, recipe)
		}
	}
	defaultVersion := firstMatch(t, makefile, `(?m)^ENVTEST_K8S_VERSION = (\S+)$`, "ENVTEST_K8S_VERSION")
	minVersion := strings.TrimSuffix(firstMatch(t, readRepoFile(t, "config/manifests/bases/"+waoCSVFile),
		`(?m)^\s+minKubeVersion: (\S+)$`, "minKubeVersion"), ".0")
	workflow := readRepoFile(t, ".github/workflows/test.yml")
	matrix := firstMatch(t, workflow, `(?m)^\s+kubernetes: \[(.*)\]$`, "test.yml Kubernetes matrix")
	for _, version := range []string{minVersion, defaultVersion} {
		if !strings.Contains(matrix, `"`+version+`"`) {
			t.Errorf("the CI matrix [%s] does not contain Kubernetes %s", matrix, version)
		}
	}
	if !strings.Contains(workflow, "make test-envtest ENVTEST_K8S_VERSION=${{ matrix.kubernetes }}") {
		t.Error("CI does not run make test-envtest for each matrix version")
	}
}
