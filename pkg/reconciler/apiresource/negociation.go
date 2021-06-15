package apiresource

import (
	"context"
	//	"errors"
	"reflect"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdnegotiation"
	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

func (c *Controller) process(key queueElement) error {
	ctx := request.WithCluster(context.TODO(), request.Cluster{Name: key.clusterName})

	switch key.theType {
	case CustomResourceDefinitionType:
		crd, err := c.crdLister.Get(key.theKey)
		if err != nil {
			var deletedObjectExists bool
			crd, deletedObjectExists = key.deletedObject.(*apiextensionsv1.CustomResourceDefinition)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || crd == nil {
				return err
			}
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// - if no NegociatedAPIResource owner
			// => Set the Enforced status condition, and then the schema of the Negociated API Resource of each CRD version
			if c.isManuallyCreatedCRD(ctx, crd) {
				if err := c.enforceCRDToNegociatedAPIResource(ctx, crd.ClusterName, key.gvr, crd); err != nil {
					return err
				}
			}
			fallthrough
		case StatusOnlyChangedAction:
			// - if NegociatedAPIResource owner
			// => Set the status (Published / Refused) on the Negociated API Resource of each CRD version

			if !c.isManuallyCreatedCRD(ctx, crd) {
				if err := c.updatePublishingStatusOnNegociatedAPIResources(ctx, crd.ClusterName, key.gvr, crd); err != nil {
					return err
				}
			}
		case Deleted:
			// - if no NegociatedAPIResource owner
			// => Delete the Negociated API Resource of each CRD version
			//    (they will be recreated from the related APIResourceImport ojects, and if requested a CRD will be created again)

			if c.isManuallyCreatedCRD(ctx, crd) {
				return c.deleteNegociatedAPIResource(ctx, crd.ClusterName, key.gvr, crd)
			} else {
				return c.updatePublishingStatusOnNegociatedAPIResources(ctx, crd.ClusterName, key.gvr, crd)
			}
		}

	case APIResourceImportType:
		apiResourceImport, err := c.apiResourceImportLister.Get(key.theKey)
		if err != nil {
			var deletedObjectExists bool
			apiResourceImport, deletedObjectExists = key.deletedObject.(*apiresourcev1alpha1.APIResourceImport)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || apiResourceImport == nil {
				return err
			}
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
			// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource. If not errors occur
			//    update the NegociatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
			// - else (NegociatedAPIResource schema update is not allowed)
			// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource.
			//    Update the current APIResourceImport status accordingly (possibly reporting errors).
			return c.ensureAPIResourceCompatibility(ctx, apiResourceImport.ClusterName, key.gvr, apiResourceImport, "")
		case StatusOnlyChangedAction:
			compatible := apiResourceImport.FindCondition(apiresourcev1alpha1.Compatible)
			available := apiResourceImport.FindCondition(apiresourcev1alpha1.Available)

			// - if both Compatible and Available conditions are unknown
			// => Do the same as if the APIResourceImport was just created or modified.
			if compatible == nil && available == nil {
				return c.ensureAPIResourceCompatibility(ctx, apiResourceImport.ClusterName, key.gvr, apiResourceImport, "")
			}
		case Deleted:
			// - If there is no other APIResourceImport for this GVR and the current negociated API resource is not enforced
			// => Delete the corresponding NegociatedAPIResource
			isOrphan, err := c.negociatedAPIResourceIsOrphan(ctx, apiResourceImport.ClusterName, key.gvr)
			if err != nil {
				return err
			}
			if isOrphan {
				return c.deleteNegociatedAPIResource(ctx, apiResourceImport.ClusterName, key.gvr, nil)
			}

			// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
			// => Calculate the LCD of all other APIResourceImports for this GVR and update the schema of the corresponding NegociatedAPIResource.
			return c.ensureAPIResourceCompatibility(ctx, apiResourceImport.ClusterName, key.gvr, apiResourceImport, apiresourcev1alpha1.UpdatePublished)
		}
	case NegociatedAPIResourceType:
		negociatedApiResource, err := c.negociatedApiResourceLister.Get(key.theKey)
		if err != nil {
			var deletedObjectExists bool
			negociatedApiResource, deletedObjectExists = key.deletedObject.(*apiresourcev1alpha1.NegociatedAPIResource)
			if !k8serrors.IsNotFound(err) || !deletedObjectExists || negociatedApiResource == nil {
				return err
			}
		}
		switch key.theAction {
		case Created, SpecChangedAction:
			// if status.Enforced
			// => Check the schema of all APIResourceImports for this GVR against the schema of the NegociatedAPIResource, and update the
			//    status of each one with the right Compatible condition.

			if negociatedApiResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
				if err := c.ensureAPIResourceCompatibility(ctx, negociatedApiResource.ClusterName, key.gvr, nil, apiresourcev1alpha1.UpdateNever); err != nil {
					return err
				}
			}

			// if spec.Published && !status.Enforced
			// => If no CRD for the corresponding GVR exists
			//    => create it with the right CRD version that corresponds to the NegociatedAPIResource spec content (schema included)
			//       and add the current NegociatedAPIResource as owner of the CRD
			//    If the CRD for the corresponding GVR exists and has a NegociatedAPIResource owner
			//    => update the CRD version of the existing CRD with the NegociatedAPIResource spec content (schema included),
			//       and add the current NegociatedAPIResource as owner of the CRD

			if negociatedApiResource.Spec.Publish && !negociatedApiResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
				if err := c.publishNegociatedResource(ctx, negociatedApiResource.ClusterName, key.gvr, negociatedApiResource); err != nil {
					return err
				}
			}
			fallthrough

		case StatusOnlyChangedAction:
			// if status == Published
			// => Udate the status of related compatible APIResourceImports, to set the `Available` condition to `true`
			return c.updateStatusOnRelatedAPIResourceImports(ctx, negociatedApiResource.ClusterName, key.gvr, negociatedApiResource)

		case Deleted:
			// if a CRD with the same GV has a version == to the current NegociatedAPIResource version *and* has the current object as owner:
			// => if this CRD version is the only one, then delete the CRD
			//    else remove this CRD version from the CRD, as well as the corresponding owner
			// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.
			return c.cleanupNegociatedAPIResource(ctx, negociatedApiResource.ClusterName, key.gvr, negociatedApiResource)
		}
	}

	return nil
}

var negociatedAPIResourceKind string = reflect.TypeOf(apiresourcev1alpha1.NegociatedAPIResource{}).Name()

func NegociatedAPIResourceAsOwnerReference(obj *apiresourcev1alpha1.NegociatedAPIResource) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiresourcev1alpha1.SchemeGroupVersion.String(),
		Kind:       negociatedAPIResourceKind,
		Name:       obj.Name,
		UID:        obj.UID,
	}
}

// isManuallyCreatedCRD detects if a CRD was created manually.
// This can be deduced from the fact that it doesn't have any NegociatedAPIResource owner reference
func (c *Controller) isManuallyCreatedCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, reference := range crd.OwnerReferences {
		if reference.APIVersion == apiresourcev1alpha1.SchemeGroupVersion.String() &&
			reference.Kind == negociatedAPIResourceKind {
			return false
		}
	}
	return true
}

// enforceCRDToNegociatedAPIResource sets the Enforced status condition,
// and then updates the schema of the Negociated API Resource of each CRD version
func (c *Controller) enforceCRDToNegociatedAPIResource(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	for _, version := range crd.Spec.Versions {
		objects, err := c.negociatedApiResourceIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			clusterName,
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
func (c *Controller) updatePublishingStatusOnNegociatedAPIResources(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	for _, version := range crd.Spec.Versions {
		objects, err := c.negociatedApiResourceIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(
			clusterName,
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

			enforcedStatus := metav1.ConditionFalse
			if c.isManuallyCreatedCRD(ctx, crd) {
				enforcedStatus = metav1.ConditionTrue
			}
			negociatedAPIResource.SetCondition(apiresourcev1alpha1.NegociatedAPIResourceCondition{
				Type:   apiresourcev1alpha1.Enforced,
				Status: enforcedStatus,
			})

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
func (c *Controller) deleteNegociatedAPIResource(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, crd *apiextensionsv1.CustomResourceDefinition) error {
	var gvrsToDelete []metav1.GroupVersionResource
	if gvr.Version != "" {
		gvrsToDelete = []metav1.GroupVersionResource{gvr}
	} else {
		if crd == nil {
			klog.Errorf("CRD is nil after deletion => no way to find the NegociatedAPIResources to delete from the CRD versions")
			return nil
		}
		for _, version := range crd.Spec.Versions {
			gvrsToDelete = append(gvrsToDelete, metav1.GroupVersionResource{
				Group:    gvr.Group,
				Version:  version.Name,
				Resource: gvr.Resource,
			})
		}
	}
	for _, gvrToDelete := range gvrsToDelete {
		objs, err := c.negociatedApiResourceIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvrToDelete))
		if err != nil {
			klog.Warningf("NegociatedAPIResource for GVR %s could not be searched in index, and could not be deleted: %v", gvr.String(), err)
		}
		if len(objs) == 0 {
			klog.Warningf("NegociatedAPIResource for GVR %s was not found and could not be deleted", gvr.String())
		}

		toDelete := objs[0].(*apiresourcev1alpha1.NegociatedAPIResource)
		err = c.apiresourceClient.NegociatedAPIResources().Delete(ctx, toDelete.Name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

// ensureAPIResourceCompatibility ensures that the given APIResourceImport (or all imports related to the GVR if the import is nil)
// is compatible with the NegociatedAPIResource. if possible and requested, it updates the NegociatedAPIResource with the LCD of the
// schemas of the various imported schemas. If no NegociatedAPIResource already exists, it can create one.
func (c *Controller) ensureAPIResourceCompatibility(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, apiResourceImport *apiresourcev1alpha1.APIResourceImport, overrideStrategy apiresourcev1alpha1.SchemaUpdateStrategyType) error {
	// - if strategy allows schema update of the negociated API resource (and current negociated API resource is not enforced)
	// => Calculate the LCD of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource. If not errors occur
	//    update the NegociatedAPIResource schema. Update the current APIResourceImport status accordingly (possibly reporting errors).
	// - else (NegociatedAPIResource schema update is not allowed)
	// => Just check the compatibility of this APIResourceImport schema against the schema of the corresponding NegociatedAPIResource.
	//    Update the current APIResourceImport status accordingly (possibly reporting errors).

	var negociatedAPIResource *apiresourcev1alpha1.NegociatedAPIResource
	objs, err := c.negociatedApiResourceIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		return err
	}
	if len(objs) > 0 {
		negociatedAPIResource = objs[0].(*apiresourcev1alpha1.NegociatedAPIResource).DeepCopy()
	}

	var apiResourcesImports []*apiresourcev1alpha1.APIResourceImport
	if apiResourceImport != nil {
		apiResourcesImports = append(apiResourcesImports, apiResourceImport)
	} else {
		objs, err := c.apiResourceImportIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
		if err != nil {
			return err
		}
		for _, obj := range objs {
			apiResourcesImports = append(apiResourcesImports, obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy())
		}
	}

	if len(apiResourcesImports) == 0 {
		return nil
	}

	var updatedNegociatedSchema bool
	newNegociatedAPIResource := negociatedAPIResource
	for _, apiResourceImport := range apiResourcesImports {
		if newNegociatedAPIResource == nil {
			name := gvr.Resource + "." + gvr.Version + "."
			if gvr.Group == "" {
				name = name + "core"
			} else {
				name = name + gvr.Group
			}

			newNegociatedAPIResource = &apiresourcev1alpha1.NegociatedAPIResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					ClusterName: clusterName,
				},
				Spec: apiresourcev1alpha1.NegociatedAPIResourceSpec{
					CommonAPIResourceSpec: apiResourceImport.Spec.CommonAPIResourceSpec,
					Publish:               c.AutoPublishNegociatedAPIResource,
				},
			}
		} else {
			allowUpdateNogociatedSchema := !negociatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Enforced) &&
				apiResourceImport.Spec.SchemaUpdateStrategy.CanUdpate(negociatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Published))

			// TODO Also check compatibility of non-schema things like group, names, short names, category, resourcescope, subresources, colums etc...

			importSchema, err := apiResourceImport.Spec.GetSchema()
			if err != nil {
				return err
			}
			negociatedSchema, err := newNegociatedAPIResource.Spec.GetSchema()
			if err != nil {
				return err
			}

			apiResourceImport = apiResourceImport.DeepCopy()
			lcd, err := crdnegotiation.LCD(field.NewPath(newNegociatedAPIResource.Spec.Kind), negociatedSchema, importSchema, allowUpdateNogociatedSchema)
			if err != nil {
				apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
					Type:    apiresourcev1alpha1.Compatible,
					Status:  metav1.ConditionFalse,
					Reason:  "IncompatibleSchema",
					Message: err.Error(),
				})
			} else {
				apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
					Type:    apiresourcev1alpha1.Compatible,
					Status:  metav1.ConditionTrue,
					Reason:  "",
					Message: "",
				})
				if allowUpdateNogociatedSchema {
					newNegociatedAPIResource.Spec.SetSchema(lcd)
					updatedNegociatedSchema = true
				}
			}
		}
		// TODO: prepare the functions here, and run them as soon as we have created / updated the NegociatedAPIResource, at the very end of the function
		if _, err := c.apiresourceClient.APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if negociatedAPIResource == nil {
		if _, err := c.apiresourceClient.NegociatedAPIResources().Create(ctx, newNegociatedAPIResource, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else if updatedNegociatedSchema {
		if _, err := c.apiresourceClient.NegociatedAPIResources().Update(ctx, newNegociatedAPIResource, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	return nil
}

// updateGVRLabel updates the apiresource.kcp.dev/gvr label according to the status of the APIResourceImport.
// This label will be useful for othe components to know if the given GVR is is published and this APIImport is compatible with it.
func (c *Controller) updateGVRLabel(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, apiResourceImport *apiresourcev1alpha1.APIResourceImport) error {
	// - if status is Compatible *and* Available
	// => add the apiresource.kcp.dev/gvr label to the GVR
	// - else
	// => remove the apiresource.kcp.dev/gvr label

	updated := apiResourceImport.DeepCopy()
	if updated.IsConditionTrue(apiresourcev1alpha1.Compatible) &&
		updated.IsConditionTrue(apiresourcev1alpha1.Available) {
		updated.Labels["apiresource.kcp.dev/gvr"] = gvr.String()
	} else {
		delete(updated.Labels, "apiresource.kcp.dev/gvr")
	}

	_, err := c.apiresourceClient.APIResourceImports().Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

// negociatedAPIResourceIsOrphan detects if there is no other APIResourceImport for this GVR and the current negociated API resource is not enforced.
func (c *Controller) negociatedAPIResourceIsOrphan(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource) (bool, error) {
	objs, err := c.apiResourceImportIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		return false, err
	}

	if len(objs) > 0 {
		return false, nil
	}

	objs, err = c.negociatedApiResourceIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		return false, err
	}
	if len(objs) != 1 {
		return false, nil
	}
	negociatedAPIResource := objs[0].(*apiresourcev1alpha1.NegociatedAPIResource)
	return !negociatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Enforced), nil
}

// publishNegociatedResource publishes the NegociatedAPIResource information as a CRD, unless a manually-added CRD already exists for this GVR
func (c *Controller) publishNegociatedResource(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	crdName := gvr.Resource
	if gvr.Group == "" {
		crdName = crdName + ".core"
	} else {
		crdName = crdName + "." + gvr.Group
	}

	negociatedSchema, err := negociatedApiResource.Spec.CommonAPIResourceSpec.GetSchema()
	if err != nil {
		return err
	}

	var subResources apiextensionsv1.CustomResourceSubresources
	for _, subResource := range negociatedApiResource.Spec.SubResources {
		if subResource.Name == "scale" {
			subResources.Scale = &apiextensionsv1.CustomResourceSubresourceScale{
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
		}
		if subResource.Name == "status" {
			subResources.Status = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}
	}

	crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
		Name:    gvr.Version,
		Storage: true, // TODO: How do we know which version will be stored ? the newest one we assume ?
		Served:  true, // TODO: Should we set served to false when the negociated API is removed, instead of removing the CRD Version or CRD itself ?
		Schema: &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: negociatedSchema,
		},
		Subresources: &subResources,
	}

	crdKey, err := cache.MetaNamespaceKeyFunc(&metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Name:        crdName,
			ClusterName: clusterName,
		},
	})
	if err != nil {
		return err
	}
	crd, err := c.crdLister.Get(crdKey)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	//  If no CRD for the corresponding GR exists
	//  => create it with the right CRD version that corresponds to the NegociatedAPIResource spec content (schema included)
	//     and add the current NegociatedAPIResource as owner of the CRD
	if k8serrors.IsNotFound(err) {
		if _, err := c.crdClient.CustomResourceDefinitions().Create(ctx, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
				OwnerReferences: []metav1.OwnerReference{
					NegociatedAPIResourceAsOwnerReference(negociatedApiResource),
				},
				ClusterName: negociatedApiResource.ClusterName,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Scope: negociatedApiResource.Spec.Scope,
				Group: negociatedApiResource.Spec.Group,
				Names: negociatedApiResource.Spec.CustomResourceDefinitionNames,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					crdVersion,
				},
			},
		}, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else if !c.isManuallyCreatedCRD(ctx, crd) {
		//  If the CRD for the corresponding GVR exists and has a NegociatedAPIResource owner
		//  => update the CRD version of the existing CRD with the NegociatedAPIResource spec content (schema included),
		//     and add the current NegociatedAPIResource as owner of the CRD

		crd = crd.DeepCopy()
		newCRDVersionIsTheLatest := true
		existingCRDVersionIndex := -1
		storageVersionIndex := -1
		for index, existingVersion := range crd.Spec.Versions {
			if existingVersion.Name == crdVersion.Name {
				existingCRDVersionIndex = index
			}
			if version.CompareKubeAwareVersionStrings(existingVersion.Name, crdVersion.Name) > 0 {
				newCRDVersionIsTheLatest = false
			}
			if existingVersion.Storage {
				storageVersionIndex = index
			}
		}

		if !newCRDVersionIsTheLatest {
			crdVersion.Storage = false
		} else if storageVersionIndex > -1 {
			crd.Spec.Versions[storageVersionIndex].Storage = false
		}

		if existingCRDVersionIndex == -1 {
			crd.Spec.Versions = append(crd.Spec.Versions, crdVersion)
		} else {
			crd.Spec.Versions[existingCRDVersionIndex] = crdVersion
		}

		var ownerReferenceAlreadyExists bool
		for _, ownerRef := range crd.OwnerReferences {
			if ownerRef.Name == negociatedApiResource.Name && ownerRef.UID == negociatedApiResource.UID {
				ownerReferenceAlreadyExists = true
				break
			}
		}

		if !ownerReferenceAlreadyExists {
			crd.OwnerReferences = append(crd.OwnerReferences,
				NegociatedAPIResourceAsOwnerReference(negociatedApiResource))
		}

		if _, err := c.crdClient.CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	// Update the NegociatedAPIResource status to Submitted

	negociatedApiResource = negociatedApiResource.DeepCopy()
	negociatedApiResource.SetCondition(apiresourcev1alpha1.NegociatedAPIResourceCondition{
		Type:   apiresourcev1alpha1.Submitted,
		Status: metav1.ConditionTrue,
	})
	if _, err := c.apiresourceClient.NegociatedAPIResources().UpdateStatus(ctx, negociatedApiResource, metav1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

// updateStatusOnRelatedAPIResourceImports udates the status of related compatible APIResourceImports, to set the `Available` condition to `true`
func (c *Controller) updateStatusOnRelatedAPIResourceImports(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	publishedCondition := negociatedApiResource.FindCondition(apiresourcev1alpha1.Published)
	if publishedCondition != nil {
		objs, err := c.apiResourceImportIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
		if err != nil {
			return err
		}
		for _, obj := range objs {
			apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
			apiResourceImport.SetCondition(apiresourcev1alpha1.APIResourceImportCondition{
				Type:   apiresourcev1alpha1.Available,
				Status: publishedCondition.Status,
			})
			if _, err := c.apiresourceClient.APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// cleanupNegociatedAPIResource does the required cleanup of related resources (CRD,APIResourceImport) after a NegociatedAPIResource has been deleted
func (c *Controller) cleanupNegociatedAPIResource(ctx context.Context, clusterName string, gvr metav1.GroupVersionResource, negociatedApiResource *apiresourcev1alpha1.NegociatedAPIResource) error {
	// In any case change the status on every APIResourceImport with the same GVR, to remove Compatible and Available conditions.

	objs, err := c.apiResourceImportIndexer.ByIndex(ClusterNameAndGVRIndexName, GetClusterNameAndGVRIndexKey(clusterName, gvr))
	if err != nil {
		return err
	}
	for _, obj := range objs {
		apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport).DeepCopy()
		apiResourceImport.RemoveCondition(apiresourcev1alpha1.Available)
		apiResourceImport.RemoveCondition(apiresourcev1alpha1.Compatible)
		if _, err := c.apiresourceClient.APIResourceImports().UpdateStatus(ctx, apiResourceImport, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	// if a CRD with the same GR has a version == to the current NegociatedAPIResource version *and* has the current object as owner:
	// => if this CRD version is the only one, then delete the CRD
	//    else remove this CRD version from the CRD, as well as the corresponding owner

	crdName := gvr.Resource
	if gvr.Group == "" {
		crdName = crdName + ".core"
	} else {
		crdName = crdName + "." + gvr.Group
	}

	crdKey, err := cache.MetaNamespaceKeyFunc(&metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Name:        crdName,
			ClusterName: clusterName,
		},
	})
	if err != nil {
		return err
	}
	crd, err := c.crdLister.Get(crdKey)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var ownerReferenceAlreadyExists bool
	var cleanedOwnerReferences []metav1.OwnerReference
	for _, ownerRef := range crd.OwnerReferences {
		if ownerRef.Name == negociatedApiResource.Name && ownerRef.UID == negociatedApiResource.UID {
			ownerReferenceAlreadyExists = true
			continue
		}
		cleanedOwnerReferences = append(cleanedOwnerReferences, ownerRef)
	}
	if !ownerReferenceAlreadyExists {
		return nil
	}

	var cleanedVersions []apiextensionsv1.CustomResourceDefinitionVersion
	for _, version := range crd.Spec.Versions {
		if version.Name == gvr.Version {
			continue
		}
		cleanedVersions = append(cleanedVersions, version)
	}
	if len(cleanedVersions) == len(crd.Spec.Versions) {
		return nil
	}
	if len(cleanedVersions) == 0 {
		if err := c.crdClient.CustomResourceDefinitions().Delete(ctx, crd.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
	} else {
		crd = crd.DeepCopy()
		crd.Spec.Versions = cleanedVersions
		crd.OwnerReferences = cleanedOwnerReferences
		if _, err := c.crdClient.CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}

	return nil
}
