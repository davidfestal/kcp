package apiresource

import (
	"context"
	"reflect"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Controller) process(key queueElement) error {
	ctx := context.TODO()

	switch key.theType {
	case CustomResourceDefinitionType:
		crd, err := c.crdLister.Get(key.theKey)
		if err != nil {
			return err
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// - if no NegociatedAPIResource owner
			// => Set the Enforced status condition, and then the schema of the Negociated API Resource of each CRD version
			if c.isManuallyCreatedCRD(ctx, crd) {
				return c.enforceCRDToNegociatedAPIResource(ctx, key.gvr, crd)
			}
		case StatusOnlyChangedAction:
			// - if NegociatedAPIResource owner
			// => Set the status (Published / Refused) on the Negociated API Resource of each CRD version

			if !c.isManuallyCreatedCRD(ctx, crd) {
				return c.updatePublishingStatusOnNegociatedAPIResources(ctx, key.gvr, crd)
			}
		case Deleted:
			// - if no NegociatedAPIResource owner
			// => Delete the Negociated API Resource of each CRD version
			//    (they will be recreated from the related APIResourceImport ojects, and if requested a CRD will be created again)

			if c.isManuallyCreatedCRD(ctx, crd) {
				return c.deleteNegociatedAPIResource(ctx, key.gvr)
			}
		}

	case APIResourceImportType:
		apiResourceImport, err := c.apiResourceImportLister.Get(key.theKey)
		if err != nil {
			return err
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
			// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource. If not errors occur
			//    update the NegociatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
			// - else (NegociatedAPIResource schema update is not allowed)
			// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource.
			//    Update the current APIResourceImport status accordingly (possibly reporting errors).
			return c.ensureAPIResourceCompatibility(ctx, key.gvr, apiResourceImport, "")
		case Deleted:
			// - If there is no other APIResourceImport for this GVR and the current negociated API resource is not enforced
			// => Delete the corresponding NegociatedAPIResource
			if c.negociatedAPIResourceIsOrphan(ctx, key.gvr) {
				return c.deleteNegociatedAPIResource(ctx, key.gvr)
			}

			// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
			// => Calculate the LCD of all other APIResourceImports for this GVR and update the schema of the corresponding NegociatedAPIResource.
			return c.ensureAPIResourceCompatibility(ctx, key.gvr, apiResourceImport, apiresourcev1alpha1.UpdatePublished)

		case StatusOnlyChangedAction:
			compatible := apiResourceImport.FindCondition(apiresourcev1alpha1.Compatible)
			available := apiResourceImport.FindCondition(apiresourcev1alpha1.Available)

			// - if both Compatible and Available conditions are unknown
			// => Do the same as if the APIResourceImport was just created or modified.
			if compatible == nil && available == nil {
				return c.ensureAPIResourceCompatibility(ctx, key.gvr, apiResourceImport, "")
			}

			// - if status is Compatible *and* Available
			// => add the apiresource.kcp.dev/gvr label to the GVR
			// - else
			// => remove the apiresource.kcp.dev/gvr label
			return c.updateGVRLabel(ctx, key.gvr, apiResourceImport)
		}
	case NegociatedAPIResourceType:
		negociatedApiResource, err := c.negociatedApiResourceLister.Get(key.theKey)
		if err != nil {
			return err
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// if status.Enforced
			// => Check the schema of all APIResourceImports for this GVR against the schema of the NegociatedAPIResource, and update the
			//    status of each one with the right Compatible condition.

			if negociatedApiResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
				return c.ensureAPIResourceCompatibility(ctx, key.gvr, nil, apiresourcev1alpha1.UpdateNever)
			}

			// if spec.Published && !status.Enforced
			// => If no CRD for the corresponding GVR exists
			//    => create it with the right CRD version that corresponds to the NegociatedAPIResource spec content (schema included)
			//       and add the current NegociatedAPIResource as owner of the CRD
			//    If the CRD for the corresponding GVR exists and has a NegociatedAPIResource owner
			//    => update the CRD version of the existing CRD with the NegociatedAPIResource spec content (schema included),
			//       and add the current NegociatedAPIResource as owner of the CRD

			if negociatedApiResource.Spec.Publish {
				return c.publishNegociatedResource(ctx, key.gvr, negociatedApiResource)
			}

		case StatusOnlyChangedAction:
			// if status == Published
			// => Udate the status of related compatible APIResourceImports, to set the `Available` condition to `true`
			return c.updateStatusOnRelatedAPIResourceImports(ctx, key.gvr, negociatedApiResource)

		case Deleted:
			// if a CRD with the same GV has a version == to the current NegociatedAPIResource version *and* has the current object as owner:
			// => if this CRD version is the only one, then delete the CRD
			//    else remove this CRD version from the CRD, as well as the corresponding owner
			// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.
			return c.cleanupNegociatedAPIResource(ctx, key.gvr, negociatedApiResource)
		}
	}

	return nil
}

var negociatedAPIResourceType reflect.Type = reflect.TypeOf(apiresourcev1alpha1.NegociatedAPIResource{})

// isManuallyCreatedCRD detects if a CRD was created manually.
// This can be deduced from the fact that it doesn't have any NegociatedAPIResource owner reference
func (c *Controller) isManuallyCreatedCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, reference := range crd.OwnerReferences {
		if reference.APIVersion == apiresourcev1alpha1.SchemeGroupVersion.String() &&
			reference.Kind == negociatedAPIResourceType.Name() {
			return true
		}
	}
	return false
}

// enforceCRDToNegociatedAPIResource sets the Enforced status condition,
// and then updates the schema of the Negociated API Resource of each CRD version
func (c *Controller) enforceCRDToNegociatedAPIResource(ctx context.Context, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	for _, version := range crd.Spec.Versions {
		objects, err := c.negociatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			crd.ClusterName,
			metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			}))
		if err != nil {
			return err
		}
		for _, obj := range objects {
			negociatedAPIResource := obj.(*apiresourcev1alpha1.NegociatedAPIResource).DeepCopy()
			negociatedAPIResource.SetCondition(apiresourcev1alpha1.NegociatedAPIResourceCondition{
				Type:   apiresourcev1alpha1.Enforced,
				Status: metav1.ConditionTrue,
			})
			negociatedAPIResource, err = c.apiresourceClient.NegociatedAPIResources().UpdateStatus(ctx, negociatedAPIResource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			// TODO: manage the case when the manually applied CRD has no schema or an invalid schema...
			negociatedAPIResource.Spec.CommonAPIResourceSpec.SetSchema(version.Schema.OpenAPIV3Schema)
			negociatedAPIResource, err = c.apiresourceClient.NegociatedAPIResources().Update(ctx, negociatedAPIResource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// updatePublishingStatusOnNegociatedAPIResources sets the status (Published / Refused) on the Negociated API Resource of each CRD version
func (c *Controller) updatePublishingStatusOnNegociatedAPIResources(ctx context.Context, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	for _, version := range crd.Spec.Versions {
		objects, err := c.negociatedApiResourceIndexer.ByIndex(clusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			crd.ClusterName,
			metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			}))
		if err != nil {
			return err
		}
		for _, obj := range objects {
			negociatedAPIResource := obj.(*apiresourcev1alpha1.NegociatedAPIResource).DeepCopy()
			if crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) &&
				crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NamesAccepted) &&
				!crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) &&
				!crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating) {
				negociatedAPIResource.SetCondition(apiresourcev1alpha1.NegociatedAPIResourceCondition{
					Type:   apiresourcev1alpha1.Published,
					Status: metav1.ConditionTrue,
				})
			} else if crdhelpers.IsCRDConditionFalse(crd, apiextensionsv1.Established) ||
				crdhelpers.IsCRDConditionFalse(crd, apiextensionsv1.NamesAccepted) ||
				crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) ||
				crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating) {
				negociatedAPIResource.SetCondition(apiresourcev1alpha1.NegociatedAPIResourceCondition{
					Type:   apiresourcev1alpha1.Published,
					Status: metav1.ConditionFalse,
				})
			}

			negociatedAPIResource, err = c.apiresourceClient.NegociatedAPIResources().UpdateStatus(ctx, negociatedAPIResource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteNegociatedAPIResource deletes the Negociated API Resource of each CRD version
// (they will be recreated from the related APIResourceImport ojects if necessary,
// and if requested a CRD will be created again as a consequence).
func (c *Controller) deleteNegociatedAPIResource(ctx context.Context, gvr metav1.GroupVersionResource) error {
	return nil
}

// ensureAPIResourceCompatibility ensures that the given APIResourceImport (or all imports related to the GVR if the import is nil)
// is compatible with the NegociatedAPIResource. if possible and requested, it updates the NegociatedAPIResource with the LCD of the
// schemas of the various imported schemas. If no NegociatedAPIResource already exists, it can create one.
func (c *Controller) ensureAPIResourceCompatibility(ctx context.Context, gvr metav1.GroupVersionResource, apiResourceImport *apiresourcev1alpha1.APIResourceImport, overrideStrategy apiresourcev1alpha1.SchemaUpdateStrategyType) error {
	// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
	// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource. If not errors occur
	//    update the NegociatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
	// - else (NegociatedAPIResource schema update is not allowed)
	// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource.
	//    Update the current APIResourceImport status accordingly (possibly reporting errors).
	return nil
}

// updateGVRLabel updates the apiresource.kcp.dev/gvr label according to the status of the APIResourceImport.
// This label will be useful for othe components to know if the given GVR is is published and this APIImport is compatible with it.
func (c *Controller) updateGVRLabel(ctx context.Context, gvr metav1.GroupVersionResource, apiResourceImport *apiresourcev1alpha1.APIResourceImport) error {
	// - if status is Compatible *and* Available
	// => add the apiresource.kcp.dev/gvr label to the GVR
	// - else
	// => remove the apiresource.kcp.dev/gvr label

	return nil
}

// negociatedAPIResourceIsOrphan detects if there is no other APIResourceImport for this GVR and the current negociated API resource is not enforced.
func (c *Controller) negociatedAPIResourceIsOrphan(ctx context.Context, gvr metav1.GroupVersionResource) bool {
	return true
}

// publishNegociatedResource publishes the NegociatedAPIResource information as a CRD, unless a manually-added CRD already exists for this GVR
func (c *Controller) publishNegociatedResource(ctx context.Context, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	//  If no CRD for the corresponding GVR exists
	//  => create it with the right CRD version that corresponds to the NegociatedAPIResource spec content (schema included)
	//     and add the current NegociatedAPIResource as owner of the CRD
	//  If the CRD for the corresponding GVR exists and has a NegociatedAPIResource owner
	//  => update the CRD version of the existing CRD with the NegociatedAPIResource spec content (schema included),
	//     and add the current NegociatedAPIResource as owner of the CRD
	return nil
}

// updateStatusOnRelatedAPIResourceImports udates the status of related compatible APIResourceImports, to set the `Available` condition to `true`
func (c *Controller) updateStatusOnRelatedAPIResourceImports(ctx context.Context, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	return nil
}

// cleanupNegociatedAPIResource does the required cleanup of related resources (CRD,APIResourceImport) after a NegociatedAPIResource has been deleted
func (c *Controller) cleanupNegociatedAPIResource(ctx context.Context, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	// if a CRD with the same GV has a version == to the current NegociatedAPIResource version *and* has the current object as owner:
	// => if this CRD version is the only one, then delete the CRD
	//    else remove this CRD version from the CRD, as well as the corresponding owner
	// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.
	return nil
}
