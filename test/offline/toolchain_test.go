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

// go.mod is the source of the Go toolchain. CI, the image build and the linter
// must select that exact patch version instead of whatever GOTOOLCHAIN=auto finds.
func TestToolchainIsPinnedEverywhere(t *testing.T) {
	toolchain := firstMatch(t, readRepoFile(t, "go.mod"), `(?m)^toolchain (go\S+)$`, "go.mod toolchain")
	version := strings.TrimPrefix(toolchain, "go")

	dockerfile := readRepoFile(t, "Dockerfile")
	if got := firstMatch(t, dockerfile, `GOTOOLCHAIN=(\S+)`, "Dockerfile"); got != toolchain {
		t.Errorf("Dockerfile GOTOOLCHAIN = %s, want %s", got, toolchain)
	}
	for _, workflow := range []string{".github/workflows/test.yml", ".github/workflows/lint.yml"} {
		text := readRepoFile(t, workflow)
		if got := firstMatch(t, text, `(?m)^\s+go-version: "?([0-9.]+)"?$`, workflow); got != version {
			t.Errorf("%s go-version = %s, want %s", workflow, got, version)
		}
		if !strings.Contains(text, "hack/verify-go-toolchain.sh") {
			t.Errorf("%s does not check the selected Go version", workflow)
		}
	}
	makefile := readRepoFile(t, "Makefile")
	if strings.Contains(firstMatch(t, makefile, `(?m)^LINT_GOTOOLCHAIN \?= (.*)$`, "LINT_GOTOOLCHAIN"), "GoVersion") {
		t.Error("LINT_GOTOOLCHAIN uses the go directive instead of the toolchain directive")
	}
}

func TestLinterVersionMatchesCI(t *testing.T) {
	local := firstMatch(t, readRepoFile(t, "Makefile"), `(?m)^GOLANGCI_LINT_VERSION = (\S+)$`, "Makefile")
	ci := firstMatch(t, readRepoFile(t, ".github/workflows/lint.yml"), `(?m)^\s+version: (\S+)$`, "lint.yml")
	if local != ci {
		t.Errorf("golangci-lint: Makefile %s, CI %s", local, ci)
	}
}
