package offline

import (
	"fmt"
	"maps"
	"strings"
	"testing"
)

func imageEntry(in operandInputs, key, env string) map[string]any {
	return in.Operands[key]["images"].(map[string]any)[env].(map[string]any)
}

func TestImageReferenceValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(operandInputs)
		valid  bool
	}{
		{"latest", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "latest"
		}, true},
		{"wao", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "wao"
		}, true},
		{"release-is-ordinary-tag", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "release"
		}, true},
		{"version", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "v0.12.0"
		}, true},
		{"registry-port", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["repository"] = "localhost:5000/medik8s/nhc"
		}, true},
		{"missing-repository", func(in operandInputs) {
			delete(imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER"), "repository")
		}, false},
		{"missing-tag", func(in operandInputs) {
			delete(imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER"), "tag")
		}, false},
		{"digest-repository", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["repository"] = "quay.io/medik8s/nhc@sha256:bad"
		}, false},
		{"tag-in-repository", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["repository"] = "quay.io/medik8s/nhc:dev"
		}, false},
		{"uri", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["repository"] = "https://quay.io/medik8s/nhc"
		}, false},
		{"tag-whitespace", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "bad tag"
		}, false},
		{"tag-colon", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = "bad:tag"
		}, false},
		{"numeric-tag", func(in operandInputs) {
			imageEntry(in, "nhc", "RELATED_IMAGE_NHC_MANAGER")["tag"] = 123
		}, false},
		{"duplicate-env", func(in operandInputs) {
			maps.Copy(in.Operands["snr"]["images"].(map[string]any), in.Operands["nhc"]["images"].(map[string]any))
		}, false},
		// Image work never reads source settings; only the copy script validates them.
		{"empty-source-path", func(in operandInputs) { in.Operands["nhc"]["source"].(map[string]any)["path"] = "" }, true},
		{"missing-source-branch", func(in operandInputs) {
			delete(in.Operands["nhc"]["source"].(map[string]any), "branch")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTree(t)
			in := tr.loadOperands()
			tc.mutate(in)
			tr.saveOperands(in)
			before := tr.snapshot("config/manager", "config/operands")
			out, err := tr.try("bash", "hack/operand-image-refs.sh")
			if tc.valid {
				if err != nil {
					t.Fatalf("valid image settings rejected: %v\n%s", err, out)
				}
				if len(strings.Split(strings.TrimSpace(out), "\n")) != 8 {
					t.Fatalf("want eight refs: %s", out)
				}
				for _, operand := range in.Operands {
					for env, raw := range operand["images"].(map[string]any) {
						img := raw.(map[string]any)
						want := fmt.Sprintf("%s\t%s:%s\n", env, img["repository"], img["tag"])
						if !strings.Contains(out, want) {
							t.Errorf("missing %q in %s", want, out)
						}
					}
				}
			} else {
				if err == nil {
					t.Fatalf("invalid settings accepted: %s", out)
				}
				if !strings.Contains(out, "nhc") && !strings.Contains(out, "RELATED_IMAGE_NHC_MANAGER") {
					t.Fatalf("diagnostic lacks operand/image: %s", out)
				}
				if out, err := tr.try("make", "-s", "operand-manifests"); err == nil {
					t.Fatalf("generation accepted invalid input: %s", out)
				}
				if !maps.Equal(before, tr.snapshot("config/manager", "config/operands")) {
					t.Fatal("invalid input changed generated settings")
				}
			}
		})
	}
}

func TestIndependentTaggedImagesStayAligned(t *testing.T) {
	tr := newTree(t)
	in := tr.loadOperands()
	for _, operand := range in.Operands {
		for env, raw := range operand["images"].(map[string]any) {
			img := raw.(map[string]any)
			img["repository"] = "localhost:5000/development/" + strings.ToLower(env)
			img["tag"] = "wao-" + strings.ToLower(env)
		}
	}
	tr.saveOperands(in)
	before := tr.snapshot("operands.yaml", "bundle")
	tr.run("make", "-s", "operand-manifests")
	for key, operand := range in.Operands {
		for env, raw := range operand["images"].(map[string]any) {
			img := raw.(map[string]any)
			want := fmt.Sprintf("%s:%s", img["repository"], img["tag"])
			query := fmt.Sprintf(`select(.kind == "Deployment") | .spec.template.spec.containers[]
 | select(.name == "manager") | .env[] | select(.name == "%s") | .value`, env)
			if got := strings.TrimSpace(tr.run("bin/yq", query, "config/manager/manager.yaml")); got != want {
				t.Errorf("%s = %s, want %s", env, got, want)
			}
			if env == "RELATED_IMAGE_FAR_MANAGER" {
				query := `select(.kind == "Deployment") | .spec.template.spec.initContainers[]?
 | select(.name == "far-agent-inventory") | .image`
				if got := strings.TrimSpace(tr.run("bin/yq", query, "config/manager/manager.yaml")); got != want {
					t.Errorf("FAR inventory image = %s, want %s", got, want)
				}
			}
			objects := string(tr.read("internal/operands/manifests/" + key + "/objects.yaml"))
			if strings.Contains(objects, want) || !strings.Contains(objects, "__WAO_"+env+"__:placeholder") {
				t.Errorf("%s runtime objects must carry the %s placeholder, not %s", key, env, want)
			}
		}
	}
	if !maps.Equal(before, tr.snapshot("operands.yaml", "bundle")) {
		t.Fatal("render changed inputs or bundle")
	}
	tr.run("go", "test", "./internal/components", "./internal/operands", "-count=1")
}

func TestImagePreflightRejectsMissingAndDuplicateDestinations(t *testing.T) {
	cases := []struct{ name, query, path string }{
		{"manager", `del(.spec.template.spec.containers[]?.env[]?
    | select(.name == "RELATED_IMAGE_NHC_MANAGER"))`, "config/manager/manager.yaml"},
		{"duplicate-manager", `(select(.kind == "Deployment") | .spec.template.spec.containers[0].env)
    += [{"name":"RELATED_IMAGE_NHC_MANAGER","value":"duplicate"}]`, "config/manager/manager.yaml"},
		{"far-inventory", `del(.spec.template.spec.initContainers[]?
    | select(.name == "far-agent-inventory"))`, "config/manager/manager.yaml"},
		{"duplicate-far-inventory", `(select(.kind == "Deployment") | .spec.template.spec.initContainers)
    += [{"name":"far-agent-inventory","image":"duplicate"}]`, "config/manager/manager.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTree(t)
			tr.run("bin/yq", "-i", tc.query, tc.path)
			before := tr.snapshot("config")
			if out, err := tr.try("bash", "hack/sync-operand-images.sh"); err == nil {
				t.Fatalf("invalid destination accepted: %s", out)
			}
			if !maps.Equal(before, tr.snapshot("config")) {
				t.Fatal("failed sync changed image settings")
			}
		})
	}
}
