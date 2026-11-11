package offline

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const workingConfig = "working-tree change\n"

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// No source commit is required: even a new checkout's working tree is usable.
func (tr *tree) sourceFixture(key string) string {
	tr.t.Helper()
	relative := filepath.Join("source checkouts", key)
	source := tr.path(relative)
	tr.run("git", "init", "-q", "--initial-branch=main", source)
	if err := os.MkdirAll(filepath.Join(source, "config"), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	tr.write(filepath.Join(relative, "config", "working.yaml"), []byte(workingConfig))
	inputs := tr.loadOperands()
	inputs.Operands[key]["source"] = map[string]any{"path": relative, "branch": "main"}
	tr.saveOperands(inputs)
	return source
}

func (tr *tree) commitSource(source string) {
	tr.t.Helper()
	tr.run("git", "-C", source, "add", "config")
	tr.run("git", "-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
		"commit", "-qm", "fixture")
}

func (tr *tree) snapshot(paths ...string) map[string]string {
	tr.t.Helper()
	files := map[string]string{}
	for _, path := range paths {
		err := filepath.WalkDir(tr.path(path), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				files[strings.TrimPrefix(p, tr.root)] = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			return nil
		})
		if err != nil {
			tr.t.Fatal(err)
		}
	}
	return files
}

func TestUpdateCopiesWorkingTreeAndPreservesOverlays(t *testing.T) {
	tr := newTree(t)
	source := tr.sourceFixture("nhc")
	unchanged := []string{
		"config/operands/nhc/overlays", "config/operands/snr", "internal/operands/manifests",
		"bundle", "operands.yaml",
	}
	before := tr.snapshot(unchanged...)
	tr.write("config/operands/nhc/config/stale.yaml", []byte("removed upstream\n"))
	tr.run("make", "-s", "update-nhc-manifests")
	if got := string(tr.read("config/operands/nhc/config/working.yaml")); got != workingConfig {
		t.Fatalf("copied content = %q", got)
	}
	if _, err := os.Stat(tr.path("config/operands/nhc/config/stale.yaml")); !os.IsNotExist(err) {
		t.Fatalf("stale file retained: %v", err)
	}
	if !maps.Equal(before, tr.snapshot(unchanged...)) {
		t.Fatal("update changed overlays, another operand, generated files, bundle, or inputs")
	}
	if _, err := os.Stat(filepath.Join(source, "config", "working.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateIncludesUncommittedAndUntrackedConfig(t *testing.T) {
	tr := newTree(t)
	source := tr.sourceFixture("nhc")
	tr.commitSource(source)
	tr.write("source checkouts/nhc/config/working.yaml", []byte("uncommitted edit\n"))
	tr.write("source checkouts/nhc/config/untracked.yaml", []byte("untracked file\n"))
	before := tr.run("git", "-C", source, "status", "--porcelain")
	tr.run("make", "-s", "update-nhc-manifests")
	if string(tr.read("config/operands/nhc/config/working.yaml")) != "uncommitted edit\n" ||
		string(tr.read("config/operands/nhc/config/untracked.yaml")) != "untracked file\n" {
		t.Fatal("update did not copy the working tree")
	}
	if after := tr.run("git", "-C", source, "status", "--porcelain"); before != after {
		t.Fatal("update modified the source checkout")
	}
}

func configureSourceFailure(tr *tree, name, source string) string {
	tr.t.Helper()
	inputs := tr.loadOperands()
	switch name {
	case "missing-checkout":
		inputs.Operands["nhc"]["source"].(map[string]any)["path"] = "missing-checkout"
	case "missing-config":
		if err := os.RemoveAll(filepath.Join(source, "config")); err != nil {
			tr.t.Fatal(err)
		}
	case "branch":
		tr.run("git", "-C", source, "symbolic-ref", "HEAD", "refs/heads/other")
		return "main"
	case "detached":
		tr.commitSource(source)
		tr.run("git", "-C", source, "checkout", "--detach", "-q")
		return "main"
	case "overlap":
		branch := strings.TrimSpace(tr.run("git", "symbolic-ref", "--short", "HEAD"))
		inputs.Operands["nhc"]["source"] = map[string]any{"path": ".", "branch": branch}
	case "not-root":
		inputs.Operands["nhc"]["source"].(map[string]any)["path"] = filepath.Join(source, "config")
	case "empty-source-path":
		inputs.Operands["nhc"]["source"].(map[string]any)["path"] = ""
		tr.saveOperands(inputs)
		return "source.path"
	case "missing-source-branch":
		delete(inputs.Operands["nhc"]["source"].(map[string]any), "branch")
		tr.saveOperands(inputs)
		return "source.branch"
	case "manager-env":
		path := "config/manager/manager.yaml"
		tr.write(path, []byte(strings.ReplaceAll(string(tr.read(path)), "RELATED_IMAGE_NHC_MANAGER", "REMOVED_IMAGE")))
		return "RELATED_IMAGE_NHC_MANAGER"
	}
	tr.saveOperands(inputs)
	return "nhc"
}

func TestUpdatePreflightPreservesAllDestinations(t *testing.T) {
	for _, name := range []string{
		"missing-checkout", "missing-config", "branch", "detached", "overlap", "not-root",
		"manager-env", "empty-source-path", "missing-source-branch",
	} {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t)
			source := tr.sourceFixture("nhc")
			diagnostic := configureSourceFailure(tr, name, source)
			paths := []string{"config/operands", "config/manager", "internal/operands/manifests", "bundle"}
			before := tr.snapshot(paths...)
			out, err := tr.try("make", "-s", "update-nhc-manifests")
			if err == nil || !strings.Contains(out, diagnostic) {
				t.Fatalf("expected preflight failure naming %s: %v\n%s", diagnostic, err, out)
			}
			if !maps.Equal(before, tr.snapshot(paths...)) {
				t.Fatal("failed preflight changed destinations")
			}
		})
	}
}

func TestUpdateRejectsUnknownOperand(t *testing.T) {
	tr := newTree(t)
	before := tr.snapshot("config")
	out, err := tr.try("bash", "hack/update-operand-manifests.sh", "unknown")
	if err == nil || !strings.Contains(out, "unknown") {
		t.Fatalf("unknown operand accepted: %v\n%s", err, out)
	}
	if !maps.Equal(before, tr.snapshot("config")) {
		t.Fatal("unknown operand changed config")
	}
}

func TestUpdateRejectsRepeatedOperandBeforeReplacingConfig(t *testing.T) {
	tr := newTree(t)
	tr.sourceFixture("nhc")
	before := tr.snapshot("config")
	out, err := tr.try("bash", "hack/update-operand-manifests.sh", "nhc", "nhc")
	if err == nil || !strings.Contains(out, "duplicate") {
		t.Fatalf("expected duplicate operand diagnostic: %v\n%s", err, out)
	}
	if !maps.Equal(before, tr.snapshot("config")) {
		t.Fatal("repeated operand changed config")
	}
}

func TestAggregateUpdateIsSequentialAndPreflightsAllSources(t *testing.T) {
	tr := newTree(t)
	keys := []string{"nhc", "snr", "sbr", "far", "mdr", "nmo"}
	for _, key := range keys {
		tr.sourceFixture(key)
	}
	// A late source failure must not replace an earlier operand.
	tr.run("git", "-C", tr.path("source checkouts/nmo"), "symbolic-ref", "HEAD", "refs/heads/other")
	before := tr.snapshot("config")
	if out, err := tr.try("make", "-s", "-j4", "update-operand-manifests"); err == nil {
		t.Fatalf("invalid last source accepted: %s", out)
	}
	if !maps.Equal(before, tr.snapshot("config")) {
		t.Fatal("aggregate changed config before validating all sources")
	}
	tr.run("git", "-C", tr.path("source checkouts/nmo"), "symbolic-ref", "HEAD", "refs/heads/main")
	out := tr.run("make", "-s", "-j4", "update-operand-manifests")
	var copied []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "== ") {
			copied = append(copied, strings.TrimSuffix(strings.Fields(line)[1], ":"))
		}
	}
	if !slices.Equal(copied, keys) {
		t.Fatalf("copy order = %v, want %v", copied, keys)
	}
	for _, key := range keys {
		if string(tr.read("config/operands/"+key+"/config/working.yaml")) != workingConfig {
			t.Fatal("missing copy for " + key)
		}
	}
}

func TestUpdateSupportsGitWorktreeAndAbsolutePath(t *testing.T) {
	tr := newTree(t)
	source := tr.sourceFixture("nhc")
	tr.commitSource(source)
	worktree := tr.path("operand worktree")
	tr.run("git", "-C", source, "worktree", "add", "-qb", "development", worktree)
	inputs := tr.loadOperands()
	inputs.Operands["nhc"]["source"] = map[string]any{"path": worktree, "branch": "development"}
	tr.saveOperands(inputs)
	tr.run("make", "-s", "update-nhc-manifests")
	if string(tr.read("config/operands/nhc/config/working.yaml")) != workingConfig {
		t.Fatal("worktree was not copied")
	}
}

func TestUpdateOnlySynchronizesSelectedImagesWithoutHiddenWork(t *testing.T) {
	tr := newTree(t)
	tr.sourceFixture("nhc")
	snrImageQuery := `select(.kind == "Deployment") | .spec.template.spec.containers[]
 | select(.name == "manager") | .env[] | select(.name == "RELATED_IMAGE_SNR_MANAGER") | .value`
	snrImageBefore := strings.TrimSpace(tr.run("bin/yq", snrImageQuery, "config/manager/manager.yaml"))
	inputs := tr.loadOperands()
	imageEntry(inputs, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "unpublished-dev"
	imageEntry(inputs, "snr", "RELATED_IMAGE_SNR_MANAGER")["tag"] = "other-unpublished"
	tr.saveOperands(inputs)
	paths := []string{"internal/operands/manifests", "bundle", "operands.yaml", "config/operands/snr", "Dockerfile"}
	before := tr.snapshot(paths...)
	mocks := tr.path("mocks")
	if err := os.Mkdir(mocks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"skopeo", "curl", "kustomize", "operator-sdk"} {
		writeExecutable(t, filepath.Join(mocks, tool), "echo unexpected-tool >&2; exit 99\n")
	}
	realGo := tr.run("sh", "-c", "command -v go")
	writeExecutable(t, filepath.Join(mocks, "go"), fmt.Sprintf(`
if [ "$1" = env ]; then exec %q "$@"; fi
echo unexpected-go >&2; exit 99
`, strings.TrimSpace(realGo)))
	realGit := strings.TrimSpace(tr.run("sh", "-c", "command -v git"))
	writeExecutable(t, filepath.Join(mocks, "git"), fmt.Sprintf(`
args=("$@"); [ "$1" != -C ] || shift 2
case "$1" in
  rev-parse|symbolic-ref) exec %q "${args[@]}" ;;
  *) echo unexpected-git >&2; exit 99 ;;
esac
`, realGit))
	tr.run("env", "PATH="+mocks+":"+os.Getenv("PATH"), "make", "-s", "update-nhc-manifests")
	if !maps.Equal(before, tr.snapshot(paths...)) {
		t.Fatal("copy ran generation or changed another operand")
	}
	manager := string(tr.read("config/manager/manager.yaml"))
	if !strings.Contains(manager, "node-healthcheck-operator:unpublished-dev") ||
		strings.TrimSpace(tr.run("bin/yq", snrImageQuery, "config/manager/manager.yaml")) != snrImageBefore {
		t.Fatal("image updates were not limited to selected operand")
	}
}
