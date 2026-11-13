package components_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
	"github.com/medik8s/workload-availability-operator/internal/components"
	"github.com/medik8s/workload-availability-operator/internal/images"
	"github.com/medik8s/workload-availability-operator/internal/operands"
)

func TestRegistrySelectsOneManagerContainer(t *testing.T) {
	imgs := images.Set{}
	for _, name := range images.Required {
		imgs[name] = "example.test/operand:test"
	}
	for _, def := range components.All() {
		t.Run(def.Key, func(t *testing.T) {
			if def.ManagerDeployment == "" || def.ManagerContainer == "" {
				t.Fatal("manager Deployment and container names must be declared")
			}
			if !slices.Contains(def.Deployments, def.ManagerDeployment) {
				t.Fatal("manager Deployment is absent from readiness and removal targets")
			}
			objects, err := operands.Render(def, "wao-test", imgs)
			if err != nil {
				t.Fatal(err)
			}
			deployments, containers := 0, 0
			for _, obj := range objects {
				if obj.GetKind() != "Deployment" || obj.GetName() != def.ManagerDeployment {
					continue
				}
				deployments++
				entries, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					name, _, err := unstructured.NestedString(entry.(map[string]any), "name")
					if err != nil {
						t.Fatal(err)
					}
					if name == def.ManagerContainer {
						containers++
					}
				}
			}
			if deployments != 1 || containers != 1 {
				t.Fatalf("selected %d Deployments and %d containers, want one of each", deployments, containers)
			}
		})
	}
}

func TestRegistryKeysMatchAPI(t *testing.T) {
	want := []string{v1alpha1.ComponentKeyNodeHealthCheck, v1alpha1.ComponentKeySelfNodeRemediation, v1alpha1.ComponentKeyStorageBasedRemediation,
		v1alpha1.ComponentKeyFenceAgentsRemediation, v1alpha1.ComponentKeyMachineDeletionRemediation, v1alpha1.ComponentKeyNodeMaintenance}
	all := components.All()
	if len(all) != len(want) {
		t.Fatalf("All() has %d entries, want %d", len(all), len(want))
	}
	for i, d := range all {
		if d.Key != want[i] {
			t.Errorf("All()[%d].Key = %q, want %q", i, d.Key, want[i])
		}
		if got, ok := components.ByKey(d.Key); !ok || got.ShortName != d.ShortName {
			t.Errorf("ByKey(%q) mismatch", d.Key)
		}
	}
	if len(want) != len(v1alpha1.AllComponentKeys) {
		t.Fatalf("registry covers %d of %d API keys", len(want), len(v1alpha1.AllComponentKeys))
	}
}

func TestEveryDefinitionMatchesItsManifests(t *testing.T) {
	for _, d := range components.All() {
		files, err := operands.Load(d.ManifestDir)
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: Load(%q) = %d files, %v", d.Key, d.ManifestDir, len(files), err)
		}
		var all strings.Builder
		for _, b := range files {
			all.Write(b)
		}
		text := all.String()
		for _, env := range d.Images {
			if !strings.Contains(text, images.Placeholder(env)) {
				t.Errorf("%s: image placeholder for %s not found in manifests", d.Key, env)
			}
		}
		for _, dep := range d.Deployments {
			if !strings.Contains(text, "name: "+dep+"\n") {
				t.Errorf("%s: deployment %s not found in manifests", d.Key, dep)
			}
		}
		for _, wh := range d.Webhooks {
			if !strings.Contains(text, "kind: "+wh.Kind+"\n") || !strings.Contains(text, "name: "+wh.Name+"\n") {
				t.Errorf("%s: webhook configuration %s/%s not found", d.Key, wh.Kind, wh.Name)
			}
		}
	}
}

func TestSNRAndSBRDeclareAgentOwners(t *testing.T) {
	snr, _ := components.ByKey(v1alpha1.ComponentKeySelfNodeRemediation)
	sbr, _ := components.ByKey(v1alpha1.ComponentKeyStorageBasedRemediation)
	nhc, _ := components.ByKey(v1alpha1.ComponentKeyNodeHealthCheck)
	if len(snr.AgentOwners) != 1 || snr.AgentOwners[0].Kind != "SelfNodeRemediationConfig" {
		t.Errorf("SNR agent owners = %+v", snr.AgentOwners)
	}
	if len(sbr.AgentOwners) != 1 || sbr.AgentOwners[0].Kind != "StorageBasedRemediationConfig" {
		t.Errorf("SBR agent owners = %+v", sbr.AgentOwners)
	}
	if len(nhc.AgentOwners) != 0 {
		t.Errorf("NHC has no agents, got %+v", nhc.AgentOwners)
	}
}

// The NHC block condition reads status.unhealthyNodes[].remediations, as the NHC webhook does.
func TestNHCAdmissionBlockReadsTheRemediationStatus(t *testing.T) {
	block := components.NHC().AdmissionBlock
	if block == nil || block.Message == "" {
		t.Fatal("NHC has no admission block with a message")
	}
	node := func(remediations ...any) map[string]any {
		return map[string]any{"name": "worker-test", "remediations": remediations}
	}
	withNodes := func(nodes ...any) map[string]any {
		return map[string]any{"status": map[string]any{"unhealthyNodes": nodes}}
	}
	remediation := map[string]any{"resource": map[string]any{"kind": "SyntheticRemediation"}}
	for name, tc := range map[string]struct {
		object map[string]any
		want   bool
	}{
		"no status":                    {map[string]any{}, false},
		"no unhealthy node":            {withNodes(), false},
		"unhealthy node, no entry":     {withNodes(map[string]any{"name": "worker-test"}), false},
		"unhealthy node, empty list":   {withNodes(node()), false},
		"one remediation":              {withNodes(node(remediation)), true},
		"second node has the entry":    {withNodes(node(), node(remediation)), true},
		"status of a different format": {map[string]any{"status": "text"}, false},
	} {
		if got := block.Blocked(tc.object); got != tc.want {
			t.Errorf("%s: Blocked = %v, want %v", name, got, tc.want)
		}
	}
	for _, d := range components.All() {
		if d.Key != v1alpha1.ComponentKeyNodeHealthCheck && d.AdmissionBlock != nil {
			t.Errorf("%s has an admission block; only NHC admission reads the object status", d.Key)
		}
	}
}

// The WAO manager serves FAR, NHC and NMO admission. OLM installs their registrations with WAO, so these
// operands render no admission configuration. The other operands keep their own webhooks.
func TestFARNHCAndNMOAdmissionIsCentral(t *testing.T) {
	central := map[string]bool{v1alpha1.ComponentKeyFenceAgentsRemediation: true, v1alpha1.ComponentKeyNodeHealthCheck: true,
		v1alpha1.ComponentKeyNodeMaintenance: true}
	wantWebhooks := map[string]int{
		v1alpha1.ComponentKeyNodeHealthCheck: 0, v1alpha1.ComponentKeySelfNodeRemediation: 2,
		v1alpha1.ComponentKeyStorageBasedRemediation: 0, v1alpha1.ComponentKeyFenceAgentsRemediation: 0,
		v1alpha1.ComponentKeyMachineDeletionRemediation: 0, v1alpha1.ComponentKeyNodeMaintenance: 0,
	}
	for _, d := range components.All() {
		if len(d.Webhooks) != wantWebhooks[d.Key] {
			t.Errorf("%s declares %d webhook configurations, want %d", d.Key, len(d.Webhooks), wantWebhooks[d.Key])
		}
		if wantCentral := central[d.Key]; d.CentralAdmission != wantCentral {
			t.Errorf("%s CentralAdmission = %v, want %v", d.Key, d.CentralAdmission, wantCentral)
		}
		data, err := os.ReadFile(filepath.Join("..", "operands", "manifests", d.ManifestDir, "objects.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		rendered := strings.Count(string(data), "kind: ValidatingWebhookConfiguration\n") +
			strings.Count(string(data), "kind: MutatingWebhookConfiguration\n")
		if rendered != wantWebhooks[d.Key] {
			t.Errorf("%s runtime manifests contain %d admission configurations, want %d", d.Key, rendered, wantWebhooks[d.Key])
		}
	}
}

func TestEveryComponentDeclaresManagedKinds(t *testing.T) {
	want := map[string]string{
		v1alpha1.ComponentKeyNodeHealthCheck: "NodeHealthCheck", v1alpha1.ComponentKeySelfNodeRemediation: "SelfNodeRemediation",
		v1alpha1.ComponentKeyStorageBasedRemediation: "StorageBasedRemediation", v1alpha1.ComponentKeyFenceAgentsRemediation: "FenceAgentsRemediation",
		v1alpha1.ComponentKeyMachineDeletionRemediation: "MachineDeletionRemediation", v1alpha1.ComponentKeyNodeMaintenance: "NodeMaintenance",
	}
	for _, d := range components.All() {
		if len(d.ManagedKinds) != 1 || d.ManagedKinds[0].Kind != want[d.Key] {
			t.Errorf("%s ManagedKinds = %+v, want %s", d.Key, d.ManagedKinds, want[d.Key])
		}
	}
}
