package offline

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type tree struct {
	t    *testing.T
	root string
}

// newTree copies the parts of the repository the operand pipeline reads into a
// fresh git checkout so the tests can mutate operands.yaml and regenerate.
func newTree(t *testing.T) *tree {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{"api", "config", "internal", "bundle", "hack"} {
		if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(src, dir))); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"Dockerfile", "Makefile", "operands.yaml", "go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(src, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"bin", "vendor"} {
		if info, err := os.Stat(filepath.Join(src, dir)); err == nil && info.IsDir() {
			if err := os.Symlink(filepath.Join(src, dir), filepath.Join(root, dir)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tr := &tree{t: t, root: root}
	tr.run("git", "init", "-q")
	return tr
}

// try runs a command in the tree and returns its combined output and error.
// Dependencies must already be vendored or cached; these tests stay offline.
func (tr *tree) try(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = tr.root
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "GIT_DIR", "GIT_WORK_TREE":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (tr *tree) run(name string, args ...string) string {
	tr.t.Helper()
	out, err := tr.try(name, args...)
	if err != nil {
		tr.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func (tr *tree) path(rel string) string { return filepath.Join(tr.root, rel) }

func (tr *tree) read(rel string) []byte {
	tr.t.Helper()
	data, err := os.ReadFile(tr.path(rel))
	if err != nil {
		tr.t.Fatal(err)
	}
	return data
}

func (tr *tree) write(rel string, data []byte) {
	tr.t.Helper()
	if err := os.WriteFile(tr.path(rel), data, 0o644); err != nil {
		tr.t.Fatal(err)
	}
}

// operandInputs contains the developer selections used by the offline pipeline.
type operandInputs struct {
	Operands map[string]map[string]any `json:"operands"`
}

func (tr *tree) loadOperands() operandInputs {
	tr.t.Helper()
	var inputs operandInputs
	if err := yaml.Unmarshal(tr.read("operands.yaml"), &inputs); err != nil {
		tr.t.Fatal(err)
	}
	return inputs
}

func (tr *tree) saveOperands(inputs operandInputs) {
	tr.t.Helper()
	data, err := json.Marshal(inputs)
	if err != nil {
		tr.t.Fatal(err)
	}
	tr.write("operands.yaml", data)
}

// Give every image an independent tag without changing any component definition.
func (tr *tree) changeTags() {
	tr.t.Helper()
	inputs := tr.loadOperands()
	i := 0
	for _, key := range slices.Sorted(maps.Keys(inputs.Operands)) {
		images := inputs.Operands[key]["images"].(map[string]any)
		for _, env := range slices.Sorted(maps.Keys(images)) {
			images[env].(map[string]any)["tag"] = fmt.Sprintf("v9.8.%d-test", i)
			i++
		}
	}
	tr.saveOperands(inputs)
}

// A tag change updates the manager Deployment and FAR build image without changing
// overlays, embedded objects or Go definitions.
func TestTagChangesEditOnlyImageDestinations(t *testing.T) {
	tr := newTree(t)
	before := tr.snapshot("internal", "config/operands")
	manager := tr.read("config/manager/manager.yaml")
	dockerfile := tr.read("Dockerfile")
	tr.changeTags()
	tr.run("make", "-s", "operand-manifests")
	if !maps.Equal(before, tr.snapshot("internal", "config/operands")) {
		t.Error("tag change edited files other than config/manager/manager.yaml")
	}
	if string(tr.read("config/manager/manager.yaml")) == string(manager) {
		t.Error("manager.yaml did not pick up the new tags")
	}
	if string(tr.read("Dockerfile")) == string(dockerfile) {
		t.Error("Dockerfile did not pick up the selected FAR image")
	}
	tr.run("go", "test", "./internal/components", "./internal/operands", "-count=1")
}

func TestVerificationAcceptsUnstagedGeneratedUpdates(t *testing.T) {
	tr := newTree(t)
	tr.run("git", "add", "config", "internal")
	tr.changeTags()
	tr.run("make", "-s", "operand-manifests")
	tr.run("make", "-s", "verify-operand-manifests")
}

func TestVerificationRejectsStaleRuntimeObjectsWithoutMutating(t *testing.T) {
	tr := newTree(t)
	tr.run("make", "-s", "operand-manifests")
	objects := "internal/operands/manifests/nhc/objects.yaml"
	placeholder := "__WAO_RELATED_IMAGE_NHC_MANAGER__:placeholder"
	stale := strings.Replace(string(tr.read(objects)), placeholder, "example.test/nhc:stale", 1)
	tr.write(objects, []byte(stale))
	out, err := tr.try("make", "-s", "verify-operand-manifests")
	if err == nil {
		t.Fatalf("verification accepted stale runtime objects:\n%s", out)
	}
	if !strings.Contains(out, "generated") {
		t.Errorf("expected a generated-files message, got:\n%s", out)
	}
	if !strings.Contains(string(tr.read(objects)), "example.test/nhc:stale") {
		t.Error("verification rewrote the stale file instead of leaving the tree as it found it")
	}
}

func TestVerificationRestoresImageDestinationsAfterGenerationFailure(t *testing.T) {
	tr := newTree(t)
	inputs := tr.loadOperands()
	imageEntry(inputs, "far", "RELATED_IMAGE_FAR_MANAGER")["tag"] = "changed-before-failure"
	tr.saveOperands(inputs)
	tr.run("bin/yq", "-i", `.resources += ["missing-resource.yaml"]`,
		"config/operands/far/overlays/runtime/kustomization.yaml")
	before := tr.snapshot("Dockerfile", "config", "internal/operands/manifests")
	out, err := tr.try("make", "-s", "verify-operand-manifests")
	if err == nil || !strings.Contains(out, "missing-resource.yaml") {
		t.Fatalf("expected an overlay generation failure: %v\n%s", err, out)
	}
	if !maps.Equal(before, tr.snapshot("Dockerfile", "config", "internal/operands/manifests")) {
		t.Fatal("failed verification changed image settings or generated manifests")
	}
}

// An upstream container whose image WAO does not map must fail generation rather than
// deploy the upstream default.
func TestOperandManifestsRejectsUnmappedImage(t *testing.T) {
	tr := newTree(t)
	path := "config/operands/nhc/overlays/runtime/kustomization.yaml"
	tr.run("bin/yq", "-i", `del(.images[] | select(.name == "quay.io/medik8s/node-remediation-console"))`, path)
	out, err := tr.try("make", "-s", "operand-manifests")
	if err == nil || !strings.Contains(out, "not a WAO placeholder") {
		t.Fatalf("expected unmapped-image failure: %v\n%s", err, out)
	}
}

// NHC enables metrics mTLS only if its ServiceAccount can read the client CA in
// kube-system/extension-apiserver-authentication. The copied Role grants that, but its
// RoleBinding names the unprefixed ServiceAccount, so WAO must rebind it or the CSV drops it.
func TestVerificationRejectsNHCWithoutClientCARead(t *testing.T) {
	tr := newTree(t)
	role := "config/operands/nhc/config/optional/kube-system-rbac/kube_system_role.yaml"
	data := strings.Replace(string(tr.read(role)), "extension-apiserver-authentication", "some-other-configmap", 1)
	tr.write(role, []byte(data))
	out, err := tr.try("make", "-s", "verify-operand-manifests")
	if err == nil {
		t.Fatalf("verification passed although NHC cannot read the client CA\n%s", out)
	}
	if !strings.Contains(out, "extension-apiserver-authentication") {
		t.Fatalf("expected a client CA check failure, got:\n%s", out)
	}
}
