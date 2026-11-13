package v1beta1

// WAO file, not copied from NMO. These tests record the NMO admission behavior
// that WAO must preserve. They call the copied validator with a fake client,
// so they need no API server. See docs/nmo-webhooks.md.

import (
	"context"
	"errors"
	"strings"
	"testing"

	nodemaintenancev1beta1 "github.com/medik8s/node-maintenance-operator/v5/api/v1beta1"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	waoWorker       = "wao-worker"
	waoControlPlane = "wao-control-plane"
)

// waoReads records the reads of the validator. A write stops the test.
type waoReads struct {
	gets []string
	// lists has one entry for each list: the kind and the namespace option.
	lists []string
	// failGet and failList, when set, replace the answer for a kind ("Node", "PodList").
	failGet, failList map[string]error
}

func waoScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, policyv1.AddToScheme, nodemaintenancev1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// waoKind names the kinds that the validator reads.
func waoKind(obj runtime.Object) string {
	switch obj.(type) {
	case *corev1.Node:
		return "Node"
	case *corev1.PodList:
		return "PodList"
	case *policyv1.PodDisruptionBudgetList:
		return "PodDisruptionBudgetList"
	case *nodemaintenancev1beta1.NodeMaintenanceList:
		return "NodeMaintenanceList"
	default:
		return "other"
	}
}

// waoValidator returns the copied validator with a fake client that has the objects.
func waoValidator(t *testing.T, isOpenShift bool, reads *waoReads, objects ...client.Object) *nodeMaintenanceValidator {
	t.Helper()
	write := errors.New("NMO admission must not write")
	cl := fake.NewClientBuilder().WithScheme(waoScheme(t)).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			kind := waoKind(obj)
			reads.gets = append(reads.gets, kind+" "+key.String())
			if err := reads.failGet[kind]; err != nil {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			kind := waoKind(list)
			options := &client.ListOptions{}
			options.ApplyOptions(opts)
			reads.lists = append(reads.lists, kind+" namespace="+options.Namespace)
			if err := reads.failList[kind]; err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			t.Error("the validator called Create")
			return write
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			t.Error("the validator called Update")
			return write
		},
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			t.Error("the validator called Patch")
			return write
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			t.Error("the validator called Delete")
			return write
		},
	}).Build()
	return &nodeMaintenanceValidator{client: cl, isOpenShift: isOpenShift}
}

func waoNode(name string, controlPlane bool) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if controlPlane {
		node.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
	}
	return node
}

func waoNM(nodeName string) *nodemaintenancev1beta1.NodeMaintenance {
	return &nodemaintenancev1beta1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{Name: "wao-maintenance"},
		Spec:       nodemaintenancev1beta1.NodeMaintenanceSpec{NodeName: nodeName, Reason: "wao-test"},
	}
}

// waoPDB returns an etcd PodDisruptionBudget in the namespace that the validator reads.
func waoPDB(name string, disruptionsAllowed int32) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: etcdQuorumPDBNamespace},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guard"}}},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: disruptionsAllowed},
	}
}

// waoGuardPod returns a guard Pod on the node. namespace selects if the validator can see it.
func waoGuardPod(namespace, nodeName string, ready corev1.ConditionStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "guard-" + nodeName, Namespace: namespace, Labels: map[string]string{"app": "guard"}},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}},
	}
}

func waoWantError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Errorf("error = %v, want an error with %q", err, text)
	}
}

// A request that the validator cannot check is rejected. The validator does not pass it.
func TestWAOCreateIsRejectedWhenAReadFails(t *testing.T) {
	forbidden := errors.New("synthetic forbidden")
	for name, tc := range map[string]struct {
		reads waoReads
		node  string
		want  string
	}{
		"node read": {waoReads{failGet: map[string]error{"Node": forbidden}}, waoWorker,
			"could not get node for validating spec.NodeName, please try again"},
		"NodeMaintenance list": {waoReads{failList: map[string]error{"NodeMaintenanceList": forbidden}}, waoWorker,
			"could not list NodeMaintenances for validating spec.NodeName, please try again"},
		// A missing RBAC rule for the etcd objects gives these two rejections (decision N1).
		"PodDisruptionBudget list": {waoReads{failList: map[string]error{"PodDisruptionBudgetList": forbidden}}, waoControlPlane,
			"synthetic forbidden"},
		"Pod list": {waoReads{failList: map[string]error{"PodList": forbidden}}, waoControlPlane,
			"synthetic forbidden"},
	} {
		t.Run(name, func(t *testing.T) {
			v := waoValidator(t, true, &tc.reads, waoNode(waoWorker, false), waoNode(waoControlPlane, true), waoPDB("etcd-guard-pdb", 0))
			_, err := v.ValidateCreate(context.Background(), waoNM(tc.node))
			waoWantError(t, err, tc.want)
		})
	}
}

// The etcd quorum rule reads only the namespace openshift-etcd, and only for a control plane node on OpenShift.
func TestWAOQuorumRuleReadsOnlyTheEtcdNamespace(t *testing.T) {
	objects := []client.Object{waoNode(waoWorker, false), waoNode(waoControlPlane, true), waoPDB("etcd-guard-pdb", 0),
		// A ready guard Pod in a different namespace must not change the answer.
		waoGuardPod("wao-other", waoControlPlane, corev1.ConditionTrue)}
	for name, tc := range map[string]struct {
		isOpenShift bool
		node        string
		wantLists   []string
	}{
		"worker on OpenShift":          {true, waoWorker, []string{"NodeMaintenanceList namespace="}},
		"control plane, not OpenShift": {false, waoControlPlane, []string{"NodeMaintenanceList namespace="}},
		"control plane on OpenShift": {true, waoControlPlane, []string{"NodeMaintenanceList namespace=",
			"PodDisruptionBudgetList namespace=openshift-etcd", "PodList namespace=openshift-etcd"}},
	} {
		t.Run(name, func(t *testing.T) {
			reads := &waoReads{}
			v := waoValidator(t, tc.isOpenShift, reads, objects...)
			if _, err := v.ValidateCreate(context.Background(), waoNM(tc.node)); err != nil {
				t.Errorf("ValidateCreate: %v", err)
			}
			if strings.Join(reads.lists, ";") != strings.Join(tc.wantLists, ";") {
				t.Errorf("lists = %v, want %v", reads.lists, tc.wantLists)
			}
			for _, get := range reads.gets {
				if get != "Node /"+tc.node {
					t.Errorf("unexpected get %q", get)
				}
			}
		})
	}
}

func TestWAOQuorumRuleAnswers(t *testing.T) {
	const violation = "will violate etcd quorum"
	for name, tc := range map[string]struct {
		objects []client.Object
		want    string // empty: the request passes
	}{
		"no PodDisruptionBudget":   {nil, violation},
		"two PodDisruptionBudgets": {[]client.Object{waoPDB("etcd-guard-pdb", 1), waoPDB("etcd-quorum-guard", 1)}, violation},
		"disruption allowed":       {[]client.Object{waoPDB("etcd-guard-pdb", 1)}, ""},
		"no disruption allowed, guard Pod ready": {[]client.Object{waoPDB("etcd-guard-pdb", 0),
			waoGuardPod(etcdQuorumPDBNamespace, waoControlPlane, corev1.ConditionTrue)}, violation},
		"no disruption allowed, guard Pod state unknown": {[]client.Object{waoPDB("etcd-guard-pdb", 0),
			waoGuardPod(etcdQuorumPDBNamespace, waoControlPlane, corev1.ConditionUnknown)}, violation},
		"no disruption allowed, guard Pod not ready": {[]client.Object{waoPDB("etcd-guard-pdb", 0),
			waoGuardPod(etcdQuorumPDBNamespace, waoControlPlane, corev1.ConditionFalse)}, ""},
		"no disruption allowed, no guard Pod on the node": {[]client.Object{waoPDB("etcd-guard-pdb", 0)}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			v := waoValidator(t, true, &waoReads{}, append(tc.objects, waoNode(waoControlPlane, true))...)
			_, err := v.ValidateCreate(context.Background(), waoNM(waoControlPlane))
			if tc.want == "" && err != nil {
				t.Errorf("ValidateCreate: %v, want a pass", err)
			}
			if tc.want != "" {
				waoWantError(t, err, tc.want)
			}
		})
	}
}

// The update rule and the delete function read no object. Thus they need no permission.
func TestWAOUpdateAndDeleteReadNoObject(t *testing.T) {
	reads := &waoReads{}
	v := waoValidator(t, true, reads)
	changed := waoNM(waoControlPlane)
	_, err := v.ValidateUpdate(context.Background(), waoNM(waoWorker), changed)
	waoWantError(t, err, "updating spec.NodeName isn't allowed")
	if _, err := v.ValidateUpdate(context.Background(), waoNM(waoWorker), waoNM(waoWorker)); err != nil {
		t.Errorf("ValidateUpdate with the same node name: %v", err)
	}
	if _, err := v.ValidateDelete(context.Background(), waoNM(waoWorker)); err != nil {
		t.Errorf("ValidateDelete: %v", err)
	}
	if len(reads.gets)+len(reads.lists) != 0 {
		t.Errorf("gets = %v, lists = %v, want no read", reads.gets, reads.lists)
	}
}
