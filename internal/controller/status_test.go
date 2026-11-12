package controller

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
)

func cond(conds []metav1.Condition, t string) metav1.ConditionStatus {
	c := meta.FindStatusCondition(conds, t)
	if c == nil {
		return "missing"
	}
	return c.Status
}

func TestSummarizeAllDisabledIsAvailable(t *testing.T) {
	conds := summarize(nil, []v1alpha1.ComponentStatus{{Name: "nodeHealthCheck", Phase: v1alpha1.ComponentPhaseDisabled}}, nil, 3)
	if cond(conds, v1alpha1.ConditionAvailable) != metav1.ConditionTrue || cond(conds, v1alpha1.ConditionProgressing) != metav1.ConditionFalse || cond(conds, v1alpha1.ConditionDegraded) != metav1.ConditionFalse {
		t.Fatalf("conditions = %+v", conds)
	}
	if meta.FindStatusCondition(conds, v1alpha1.ConditionAvailable).ObservedGeneration != 3 {
		t.Fatal("observedGeneration not propagated")
	}
}

func TestSummarizeProgressingComponent(t *testing.T) {
	conds := summarize(nil, []v1alpha1.ComponentStatus{{Name: "a", Phase: v1alpha1.ComponentPhaseAvailable}, {Name: "b", Phase: v1alpha1.ComponentPhaseProgressing}}, nil, 1)
	if cond(conds, v1alpha1.ConditionProgressing) != metav1.ConditionTrue || cond(conds, v1alpha1.ConditionAvailable) != metav1.ConditionFalse {
		t.Fatalf("conditions = %+v", conds)
	}
}

func TestSummarizeDegradedOnErrorOrDegradedComponent(t *testing.T) {
	conds := summarize(nil, []v1alpha1.ComponentStatus{{Name: "a", Phase: v1alpha1.ComponentPhaseAvailable}}, errors.New("boom"), 1)
	if cond(conds, v1alpha1.ConditionDegraded) != metav1.ConditionTrue || cond(conds, v1alpha1.ConditionAvailable) != metav1.ConditionFalse {
		t.Fatalf("conditions = %+v", conds)
	}
	conds = summarize(nil, []v1alpha1.ComponentStatus{{Name: "a", Phase: v1alpha1.ComponentPhaseDegraded}}, nil, 1)
	if cond(conds, v1alpha1.ConditionDegraded) != metav1.ConditionTrue {
		t.Fatalf("conditions = %+v", conds)
	}
}

func TestSummarizeDisableIncompleteIsAvailableButFlagged(t *testing.T) {
	conds := summarize(nil, []v1alpha1.ComponentStatus{{Name: "snr", Phase: v1alpha1.ComponentPhaseDisableIncomplete}}, nil, 1)
	if cond(conds, v1alpha1.ConditionAvailable) != metav1.ConditionTrue || cond(conds, v1alpha1.ConditionDegraded) != metav1.ConditionFalse {
		t.Fatalf("conditions = %+v", conds)
	}
	if meta.FindStatusCondition(conds, v1alpha1.ConditionAvailable).Reason != "DisableIncomplete" {
		t.Fatalf("reason = %q", meta.FindStatusCondition(conds, v1alpha1.ConditionAvailable).Reason)
	}
}

func TestSummarizeKeepsTransitionTimeWhenUnchanged(t *testing.T) {
	disabled := []v1alpha1.ComponentStatus{{Name: "a", Phase: v1alpha1.ComponentPhaseDisabled}}
	first := summarize(nil, disabled, nil, 1)
	old := metav1.NewTime(metav1.Now().Add(-time.Hour))
	for i := range first {
		first[i].LastTransitionTime = old
	}
	second := summarize(first, disabled, nil, 1)
	for _, c := range second {
		if !c.LastTransitionTime.Equal(&old) {
			t.Fatalf("%s lastTransitionTime changed although its status did not", c.Type)
		}
	}
	third := summarize(second, []v1alpha1.ComponentStatus{{Name: "a", Phase: v1alpha1.ComponentPhaseProgressing}}, nil, 2)
	if meta.FindStatusCondition(third, v1alpha1.ConditionProgressing).LastTransitionTime.Equal(&old) {
		t.Fatal("Progressing changed status but kept its old lastTransitionTime")
	}
}

// Without an injected callback, central FAR admission is not ready.
func TestCentralAdmissionReadyDefaultsToFalse(t *testing.T) {
	if (&WorkloadAvailabilityConfigReconciler{}).centralAdmissionReady() {
		t.Error("a nil FARAdmissionReady function reports ready")
	}
	ready := &WorkloadAvailabilityConfigReconciler{FARAdmissionReady: func() bool { return true }}
	if !ready.centralAdmissionReady() {
		t.Error("the injected function is not used")
	}
}
