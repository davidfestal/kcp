package crdnegotiation

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func LCD(fldPath *field.Path, existing, new *apiextensionsv1.JSONSchemaProps, narrowExisting bool) (lcd *apiextensionsv1.JSONSchemaProps, errors utilerrors.Aggregate) {
	var newInternal, existingInternal apiextensions.JSONSchemaProps
	apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(existing, &existingInternal, nil)
	apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(new, &newInternal, nil)
	newStrucural, err := schema.NewStructural(&newInternal)
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}

	existingStructural, err := schema.NewStructural(&existingInternal)
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}

	lcdStructural := existingStructural.DeepCopy()
	errs := lcdForStructural(fldPath, existingStructural, newStrucural, lcdStructural, narrowExisting)
	if len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	serialized, err := json.Marshal(lcdStructural.ToGoOpenAPI())
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}
	var jsonSchemaProps apiextensionsv1.JSONSchemaProps
	json.Unmarshal(serialized, &jsonSchemaProps)
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}
	return &jsonSchemaProps, nil
}

func checkTypesAreTheSame(fldPath *field.Path, existing, new *schema.Structural) (errorList field.ErrorList) {
	if new.Type != existing.Type {
		return field.ErrorList{field.Invalid(fldPath.Child("type"), new, "The type of the should not be changed")}
	}
	return nil
}

func checkUnsupportedValidation(fldPath *field.Path, existing, new interface{}, validationName, typeName string) (errorList field.ErrorList) {
	if !reflect.ValueOf(existing).IsZero() || !reflect.ValueOf(new).IsZero() {
		return field.ErrorList{field.Forbidden(fldPath, fmt.Sprintf("The '%s' JSON Schema construct is not supported by the Schema negociation for type '%s'", validationName, typeName))}
	}
	return nil
}

func checkUnsupportedValidationForNumerics(fldPath *field.Path, existing, new *schema.ValueValidation, typeName string) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Not, new.Not, "not", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", typeName)...)
	if !(new.Maximum == existing.Maximum && new.Minimum == existing.Minimum && new.ExclusiveMaximum == existing.ExclusiveMaximum && new.ExclusiveMinimum == existing.ExclusiveMinimum) {
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Maximum, new.Maximum, "maximum", typeName)...)
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Minimum, new.Minimum, "minimum", typeName)...)
	}
	if new.MultipleOf != existing.MultipleOf {
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.MultipleOf, new.MultipleOf, "multipleOf", typeName)...)
	}
	return
}

func lcdForStructural(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	if lcd == nil && narrowExisting {
		return field.ErrorList{field.InternalError(fldPath, errors.New("lcd argument should be passed when narrowExisting is true"))}
	}
	if new == nil {
		return field.ErrorList{field.Invalid(fldPath, new, "new schema doesn't allow anything")}
	}
	if existing.XPreserveUnknownFields != new.XPreserveUnknownFields {
		errorList = append(errorList, field.Invalid(fldPath.Child("x-preserve-unknown-fields"), new, "x-preserve-unknown-fields value has been changed in an incompatible way"))
	}

	switch existing.Type {
	case "number":
		return lcdForNumber(fldPath, existing, new, lcd, narrowExisting)
	case "integer":
		return lcdForInteger(fldPath, existing, new, lcd, narrowExisting)
	case "string":
		return lcdForString(fldPath, existing, new, lcd, narrowExisting)
	case "boolean":
		return lcdForBoolean(fldPath, existing, new, lcd, narrowExisting)
	case "array":
		return lcdForArray(fldPath, existing, new, lcd, narrowExisting)
	case "object":
		return lcdForObject(fldPath, existing, new, lcd, narrowExisting)
	case "":
		if existing.XIntOrString {
			return lcdForIntOrString(fldPath, existing, new, lcd, narrowExisting)
		} else if existing.XPreserveUnknownFields {
			return lcdForPreserveUnknownFields(fldPath, existing, new, lcd, narrowExisting)
		}
	}
	return field.ErrorList{field.Invalid(field.NewPath(fldPath.String(), "type"), existing, "Invalid type")}
}

func lcdForIntegerValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidationForNumerics(fldPath, existing, new, "integer")...)
	return
}

func lcdForNumberValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidationForNumerics(fldPath, existing, new, "numbers")...)
	return
}

func lcdForNumber(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	if new.Type == "integer" {
		// new type is a subset of the existing type.
		if !narrowExisting {
			errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
			return
		}
		lcd.Type = new.Type
		errorList = append(errorList, lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
		return
	}

	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	if len(errorList) > 0 {
		return
	}

	errorList = append(errorList, lcdForNumberValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	return
}

func lcdForInteger(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	if new.Type == "number" {
		// new type is a superset of the existing type.
		// all is well type-wise
		// keep the existing type (integer) in the LCD
	} else {
		errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
		if len(errorList) > 0 {
			return
		}
	}
	errorList = append(errorList, lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	return
}

func lcdForStringValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "string")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "string")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "string")...)
	if !(new.MaxLength == existing.MaxLength && new.MinLength == existing.MinLength) {
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.MaxLength, new.MaxLength, "maxLength", "string")...)
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.MinLength, new.MinLength, "minLength", "string")...)
	}
	if new.Pattern != existing.Pattern {
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Pattern, new.Pattern, "pattern", "string")...)
	}
	toEnumSets := func(enum []schema.JSON) sets.String {
		enumSet := sets.NewString()
		for _, val := range enum {
			strVal, isString := val.Object.(string)
			if !isString {
				errorList = append(errorList, field.Invalid(fldPath.Child("enum"), enum, "enum value should be a 'string' for Json type 'string'"))
				continue
			}
			enumSet.Insert(strVal)
		}
		return enumSet
	}
	existingEnumValues := toEnumSets(existing.Enum)
	newEnumValues := toEnumSets(new.Enum)
	if !newEnumValues.IsSuperset(existingEnumValues) {
		if !narrowExisting {
			errorList = append(errorList, field.Invalid(fldPath.Child("enum"), new.Enum, "enum value has been changed in an incompatible way"))
		}
		lcd.Enum = nil
		lcdEnumValues := existingEnumValues.Intersection(newEnumValues).List()
		for _, val := range lcdEnumValues {
			lcd.Enum = append(lcd.Enum, schema.JSON{Object: val})
		}
	} else {
		// new enum values is a superset of existing enum values, so let the LCD keep the existing enum values
	}

	if existing.Format != new.Format {
		errorList = append(errorList, field.Invalid(fldPath.Child("format"), new.Format, "format value has been changed in an incompatible way"))
	}
	return
}

func lcdForString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	errorList = append(errorList, lcdForStringValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	return
}

func lcdForBooleanValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "boolean")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "boolean")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "boolean")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "boolean")...)
	return
}

func lcdForBoolean(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	errorList = append(errorList, lcdForBooleanValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	return
}

func lcdForArrayValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "array")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "array")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "array")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "array")...)
	if !(new.MaxItems == existing.MaxItems && new.MinItems == existing.MinItems) {
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.MaxLength, new.MaxLength, "maxItems", "array")...)
		errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.MinLength, new.MinLength, "minItems", "array")...)
	}
	if !existing.UniqueItems && new.UniqueItems {
		if !narrowExisting {
			errorList = append(errorList, field.Invalid(fldPath.Child("uniqueItems"), new.UniqueItems, "uniqueItems value has been changed in an incompatible way"))
		} else {
			lcd.UniqueItems = true
		}
	}
	return
}

func valuesEqual(v1, v2 *string) bool {
	if v1 == nil && v2 == nil {
		return true
	}
	if v1 != nil && v2 != nil {
		return *v1 == *v2
	}
	return false
}

func lcdForArray(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	errorList = append(errorList, lcdForArrayValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	errorList = append(errorList, lcdForStructural(fldPath.Child("Items"), existing.Items, new.Items, lcd.Items, narrowExisting)...)
	if !valuesEqual(existing.Extensions.XListType, new.Extensions.XListType) {
		errorList = append(errorList, field.Invalid(fldPath.Child("x-kubernetes-list-type"), new.Extensions.XListType, "x-kubernetes-list-type value has been changed in an incompatible way"))
	}
	if !sets.NewString(existing.Extensions.XListMapKeys...).Equal(sets.NewString(new.Extensions.XListMapKeys...)) {
		errorList = append(errorList, field.Invalid(fldPath.Child("x-kubernetes-list-map-keys"), new.Extensions.XListType, "x-kubernetes-list-map-keys value has been changed in an incompatible way"))
	}
	return
}

func lcdForObjectValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "object")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "object")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "object")...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "object")...)
	return
}

func lcdForObject(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)

	if existing.Extensions.XMapType != new.Extensions.XMapType {
		errorList = append(errorList, field.Invalid(fldPath.Child("x-kubernetes-map-type"), new.Extensions.XListType, "x-kubernetes-map-type value has been changed in an incompatible way"))
	}

	// Let's keep in mind that, in structural schemas, properties and additionalProperties are mutually exclusive,
	// which greatly simplifies the logic here.

	if len(existing.Properties) > 0 {
		if len(new.Properties) > 0 {
			existingProperties := sets.StringKeySet(existing.Properties)
			newProperties := sets.StringKeySet(new.Properties)
			lcdProperties := existingProperties
			if !newProperties.IsSuperset(existingProperties) {
				if !narrowExisting {
					errorList = append(errorList, field.Invalid(fldPath.Child("properties"), new.Properties, "properties value has been changed in an incompatible way"))
				}
				lcdProperties = existingProperties.Intersection(newProperties)
			}
			for _, key := range lcdProperties.UnsortedList() {
				existingPropertySchema := existing.Properties[key]
				newPropertySchema := new.Properties[key]
				lcdPropertySchema := lcd.Properties[key]
				errorList = append(errorList, lcdForStructural(fldPath.Child("properties").Key(key), &existingPropertySchema, &newPropertySchema, &lcdPropertySchema, narrowExisting)...)
				lcd.Properties[key] = lcdPropertySchema
			}
			for _, removedProperty := range existingProperties.Difference(lcdProperties).UnsortedList() {
				delete(lcd.Properties, removedProperty)
			}

		} else if new.AdditionalProperties.Structural != nil {
			for key, existingPropertySchema := range existing.Properties {
				lcdPropertySchema := lcd.Properties[key]
				errorList = append(errorList, lcdForStructural(fldPath.Child("properties").Key(key), &existingPropertySchema, new.AdditionalProperties.Structural, &lcdPropertySchema, narrowExisting)...)
				lcd.Properties[key] = lcdPropertySchema
			}
		} else if new.AdditionalProperties.Bool {
			// that allows named properties only.
			// => Keep the existing schemas as the lcd.
		} else {
			errorList = append(errorList, field.Invalid(fldPath.Child("properties"), new.Properties, "properties value has been completely cleared in an incompatible way"))
		}
	} else if existing.AdditionalProperties != nil {
		if existing.AdditionalProperties.Structural != nil {
			if new.AdditionalProperties.Structural != nil {
				errorList = append(errorList, lcdForStructural(fldPath.Child("additionalProperties"), existing.AdditionalProperties.Structural, new.AdditionalProperties.Structural, lcd.AdditionalProperties.Structural, narrowExisting)...)
			} else if existing.AdditionalProperties != nil && new.AdditionalProperties.Bool {
				// new schema allows any properties of any schema here => it is a superset of the existing schema
				// that allows any properties of a given schema.
				// => Keep the existing schemas as the lcd.
			} else {
				errorList = append(errorList, field.Invalid(fldPath.Child("additionalProperties"), new.AdditionalProperties, "additionalProperties value has been changed in an incompatible way"))
			}
		} else if existing.AdditionalProperties.Bool {
			if !new.AdditionalProperties.Bool {
				if !narrowExisting {
					errorList = append(errorList, field.Invalid(fldPath.Child("additionalProperties"), new.AdditionalProperties, "additionalProperties value has been changed in an incompatible way"))
				}
				lcd.AdditionalProperties.Bool = false
				lcd.AdditionalProperties.Structural = new.AdditionalProperties.Structural
			}
		}
	}  else {
		// Existing schema doesn't allow anything => new will always be a superset of existing
	}

	return
}

func lcdForIntOrString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	if !new.XIntOrString {
		errorList = append(errorList, field.Invalid(fldPath.Child("x-kubernetes-int-or-string"), new, "x-kubernetes-int-or-string value has been changed in an incompatible way"))
	}

	errorList = append(errorList, lcdForStringValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	errorList = append(errorList, lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)

	return
}

func lcdForPreserveUnknownFields(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	return
}
