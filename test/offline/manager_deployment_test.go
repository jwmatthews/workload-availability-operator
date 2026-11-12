package offline

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const (
	farInventoryName  = "far-agent-inventory"
	farInventoryDir   = "/var/run/wao/far-agent-inventory"
	farInventoryImage = "quay.io/medik8s/fence-agents-remediation-operator:wao"
)

// The collector command from docs/far-webhooks.md. It reads directory entries only.
const farCollectorScript = `set -eu
out=/var/run/wao/far-agent-inventory
cd /usr/sbin
ls -1d -- fence_* > "$out/agents.txt.tmp"
mv "$out/agents.txt.tmp" "$out/agents.txt"
`

func readManagerDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	for _, doc := range strings.Split(readRepoFile(t, "config/manager/manager.yaml"), "\n---\n") {
		if !strings.Contains(doc, "kind: Deployment") {
			continue
		}
		deployment := &appsv1.Deployment{}
		if err := yaml.UnmarshalStrict([]byte(doc), deployment); err != nil {
			t.Fatal(err)
		}
		return deployment
	}
	t.Fatal("config/manager/manager.yaml has no Deployment")
	return nil
}

func TestManagerRunsTwoReplicasWithLeaderElection(t *testing.T) {
	deployment := readManagerDeployment(t)
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 2 {
		t.Errorf("replicas = %v, want 2", deployment.Spec.Replicas)
	}
	manager := deployment.Spec.Template.Spec.Containers[0]
	if !slices.Contains(manager.Args, "--leader-elect") {
		t.Error("the manager does not use --leader-elect")
	}
	affinity := deployment.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.PodAntiAffinity == nil {
		t.Fatal("no Pod anti-affinity")
	}
	if len(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 0 {
		t.Error("the anti-affinity must be a preference, so both replicas can start on one node")
	}
	preferred := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) != 1 || preferred[0].PodAffinityTerm.TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("preferred anti-affinity = %+v, want one kubernetes.io/hostname term", preferred)
	}
	selector := preferred[0].PodAffinityTerm.LabelSelector
	if selector == nil || len(selector.MatchLabels) == 0 {
		t.Fatal("the anti-affinity term has no label selector")
	}
	for key, value := range selector.MatchLabels {
		if deployment.Spec.Template.Labels[key] != value {
			t.Errorf("anti-affinity label %s=%s does not match the Pod labels", key, value)
		}
	}
}

func TestManagerCollectsFARInventoryBeforeStart(t *testing.T) {
	spec := readManagerDeployment(t).Spec.Template.Spec
	if len(spec.InitContainers) != 1 || spec.InitContainers[0].Name != farInventoryName {
		t.Fatalf("init containers = %+v, want exactly %s", spec.InitContainers, farInventoryName)
	}
	collector := spec.InitContainers[0]
	if collector.Image != farInventoryImage {
		t.Errorf("collector image = %s, want %s", collector.Image, farInventoryImage)
	}
	// The collector overrides the image entrypoint. It never starts the FAR manager or a fence agent.
	if !slices.Equal(collector.Command, []string{"sh", "-c"}) ||
		!slices.Equal(collector.Args, []string{farCollectorScript}) {
		t.Errorf("collector command = %q %q", collector.Command, collector.Args)
	}
	if collector.RestartPolicy != nil {
		t.Error("the collector must be a plain init container, so a failure prevents the manager start")
	}
	if collector.Resources.Requests.Cpu().IsZero() || collector.Resources.Limits.Memory().IsZero() {
		t.Error("the collector has no resource requests and limits")
	}

	var volume *corev1.Volume
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == farInventoryName {
			volume = &spec.Volumes[i]
		}
	}
	if volume == nil || volume.EmptyDir == nil {
		t.Fatalf("no emptyDir volume %s", farInventoryName)
	}
	mount := func(c corev1.Container) *corev1.VolumeMount {
		for i := range c.VolumeMounts {
			if c.VolumeMounts[i].Name == farInventoryName {
				return &c.VolumeMounts[i]
			}
		}
		return nil
	}
	if m := mount(collector); m == nil || m.MountPath != farInventoryDir || m.ReadOnly {
		t.Errorf("collector mount = %+v, want a writable mount at %s", m, farInventoryDir)
	}
	manager := spec.Containers[0]
	if m := mount(manager); m == nil || m.MountPath != farInventoryDir || !m.ReadOnly {
		t.Errorf("manager mount = %+v, want a read-only mount at %s", m, farInventoryDir)
	}
	if !slices.Contains(manager.Args, "--far-agent-inventory="+farInventoryDir+"/agents.txt") {
		t.Errorf("manager args %q do not select the inventory file", manager.Args)
	}
}

func TestManagerPodKeepsRestrictedSecuritySettings(t *testing.T) {
	spec := readManagerDeployment(t).Spec.Template.Spec
	psc := spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot ||
		psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("the Pod security context is not restricted")
	}
	// No fixed UID: OpenShift assigns an arbitrary UID from the namespace range.
	if psc != nil && psc.RunAsUser != nil {
		t.Error("the Pod sets a fixed UID")
	}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
			sc.Capabilities == nil || !slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")) {
			t.Errorf("container %s is not restricted: %+v", c.Name, sc)
		}
		if sc != nil && (sc.RunAsUser != nil || (sc.Privileged != nil && *sc.Privileged)) {
			t.Errorf("container %s sets a fixed UID or is privileged", c.Name)
		}
	}
}
