package v1alpha1

import (
	"time"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SetCondition sets the status condition. It either overwrites the existing one or creates a new one.
func (negociatedApiResource *NegociatedAPIResource) SetCondition(newCondition NegociatedAPIResourceCondition) {
	newCondition.LastTransitionTime = metav1.NewTime(time.Now())

	existingCondition := negociatedApiResource.FindCondition(newCondition.Type)
	if existingCondition == nil {
		negociatedApiResource.Status.Conditions = append(negociatedApiResource.Status.Conditions, newCondition)
		return
	}

	if existingCondition.Status != newCondition.Status || existingCondition.LastTransitionTime.IsZero() {
		existingCondition.LastTransitionTime = newCondition.LastTransitionTime
	}

	existingCondition.Status = newCondition.Status
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

// RemoveCondition removes the status condition.
func (negociatedApiResource *NegociatedAPIResource) RemoveCondition(conditionType NegociatedAPIResourceConditionType) {
	newConditions := []NegociatedAPIResourceCondition{}
	for _, condition := range negociatedApiResource.Status.Conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}
	negociatedApiResource.Status.Conditions = newConditions
}

// FindCondition returns the condition you're looking for or nil.
func (negociatedApiResource *NegociatedAPIResource) FindCondition(conditionType NegociatedAPIResourceConditionType) *NegociatedAPIResourceCondition {
	for i := range negociatedApiResource.Status.Conditions {
		if negociatedApiResource.Status.Conditions[i].Type == conditionType {
			return &negociatedApiResource.Status.Conditions[i]
		}
	}

	return nil
}

// IsConditionTrue indicates if the condition is present and strictly true.
func (negociatedApiResource *NegociatedAPIResource) IsConditionTrue(conditionType NegociatedAPIResourceConditionType) bool {
	return negociatedApiResource.IsConditionPresentAndEqual(conditionType, metav1.ConditionTrue)
}

// IsConditionFalse indicates if the condition is present and false.
func (negociatedApiResource *NegociatedAPIResource) IsConditionFalse(conditionType NegociatedAPIResourceConditionType) bool {
	return negociatedApiResource.IsConditionPresentAndEqual(conditionType, metav1.ConditionFalse)
}

// IsConditionPresentAndEqual indicates if the condition is present and equal to the given status.
func (negociatedApiResource *NegociatedAPIResource) IsConditionPresentAndEqual(conditionType NegociatedAPIResourceConditionType, status metav1.ConditionStatus) bool {
	for _, condition := range negociatedApiResource.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsNegociatedAPIResourceConditionEquivalent returns true if the lhs and rhs are equivalent except for times.
func IsNegociatedAPIResourceConditionEquivalent(lhs, rhs *NegociatedAPIResourceCondition) bool {
	if lhs == nil && rhs == nil {
		return true
	}
	if lhs == nil || rhs == nil {
		return false
	}

	return lhs.Message == rhs.Message && lhs.Reason == rhs.Reason && lhs.Status == rhs.Status && lhs.Type == rhs.Type
}

// GVR returns the GVR that this NegociatedAPIResource represents.
func (negociatedApiResource *NegociatedAPIResource) GVR() metav1.GroupVersionResource {
	return metav1.GroupVersionResource {
		Group: negociatedApiResource.Spec.Group,
		Version: negociatedApiResource.Spec.Version,
		Resource: negociatedApiResource.Spec.Plural,
	}
}
