package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/workload-availability-operator/api/v1alpha1"
)

// summarize derives the three top-level conditions from component statuses, starting from the
// existing conditions so an unchanged condition keeps its lastTransitionTime. That keeps repeated
// reconciles from rewriting status, which would otherwise retrigger the controller in a loop.
// Available=True means WAO reconciled successfully and no enabled component is still
// progressing or degraded; it does not claim that workloads are protected.
func summarize(existing []metav1.Condition, components []v1alpha1.ComponentStatus, reconcileErr error, generation int64) []metav1.Condition {
	progressing, degraded, incomplete := false, reconcileErr != nil, false
	for _, c := range components {
		switch c.Phase {
		case v1alpha1.ComponentPhaseProgressing:
			progressing = true
		case v1alpha1.ComponentPhaseDegraded:
			degraded = true
		case v1alpha1.ComponentPhaseDisableIncomplete:
			incomplete = true
		}
	}
	set := func(conds *[]metav1.Condition, t string, status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(conds, metav1.Condition{Type: t, Status: status, Reason: reason, Message: msg, ObservedGeneration: generation})
	}
	conds := append([]metav1.Condition(nil), existing...)
	switch {
	case degraded:
		msg := "one or more components failed to reconcile"
		if reconcileErr != nil {
			msg = reconcileErr.Error()
		}
		set(&conds, v1alpha1.ConditionDegraded, metav1.ConditionTrue, "ReconcileError", msg)
	default:
		set(&conds, v1alpha1.ConditionDegraded, metav1.ConditionFalse, "AsExpected", "")
	}
	if progressing {
		set(&conds, v1alpha1.ConditionProgressing, metav1.ConditionTrue, "ComponentsProgressing", "operand rollout, shutdown, or admission readiness pending")
	} else {
		set(&conds, v1alpha1.ConditionProgressing, metav1.ConditionFalse, "AsExpected", "")
	}
	switch {
	case degraded:
		set(&conds, v1alpha1.ConditionAvailable, metav1.ConditionFalse, "Degraded", "see Degraded condition")
	case progressing:
		set(&conds, v1alpha1.ConditionAvailable, metav1.ConditionFalse, "Progressing", "see Progressing condition")
	case incomplete:
		set(&conds, v1alpha1.ConditionAvailable, metav1.ConditionTrue, "DisableIncomplete", "a disabled component still has retained agents or configs; see status.components")
	default:
		set(&conds, v1alpha1.ConditionAvailable, metav1.ConditionTrue, "AsExpected", "")
	}
	return conds
}
