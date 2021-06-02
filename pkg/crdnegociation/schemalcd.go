package crdnegociation

import (
	"encoding/json"
	"errors"
	"fmt"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func LCD(fldPath *field.Path, existing, new *apiextensions.JSONSchemaProps, narrowExisting bool) (lcd *apiextensions.JSONSchemaProps, errors utilerrors.Aggregate) {
	newStrucural, err := schema.NewStructural(new)
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}

	existingStructural, err := schema.NewStructural(existing)
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
	var jsonSchemaProps apiextensions.JSONSchemaProps
	json.Unmarshal(serialized, &jsonSchemaProps)
	if err != nil {
		return nil, utilerrors.NewAggregate([]error{err})
	}
	return &jsonSchemaProps, nil
}

type SchemaCompareStrategy string
const (
	Narrow SchemaCompareStrategy = "Narrow"
	Widen SchemaCompareStrategy = "Widen"
	CompareOnly SchemaCompareStrategy = "CompareOnly"
)

type SchemaCompareResult string
const (
	SubSchema SchemaCompareResult = "SubSchema"
	SuperSchema SchemaCompareResult = "SuperSchema"
	IncompatibleSchemas SchemaCompareResult = "Incompatibleschema"
)

type StructuralSchema interface {
	inhabited() bool
	compare(fldPath *field.Path, other StructuralSchema, strategy SchemaCompareStrategy) 
}

type BaseStructuralSchema schema.Structural
type NumericStructuralSchema schema.Structural
type IntegerStructuralSchema schema.Structural
type NumberStructuralSchema schema.Structural
type StringStructuralSchema schema.Structural


func checkTypesAreTheSame(fldPath *field.Path, existing, new *schema.Structural) (errorList field.ErrorList) {
	if new.Type != existing.Type {
		return field.ErrorList { field.Invalid(fldPath.Child("type"), new, "The type of the should not be changed") }
	}
	return nil
}

func checkUnsupportedValidation(fldPath *field.Path, existing, new interface{}, validationName, typeName string) (errorList field.ErrorList) {
	if existing != nil || new != nil {
		return field.ErrorList { field.Forbidden(fldPath, fmt.Sprintf("The '%s' JSON Schema construct is not supported by the Schema negociation for type '%s'", validationName, typeName)) }
	}
	return nil
}

func checkUnsupportedValidationForNumerics(fldPath *field.Path, existing, new *schema.ValueValidation, typeName string) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Not, new.Not, "not", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", typeName)...)
	errorList = append(errorList, checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", typeName)...)
	if ! (new.Maximum == existing.Maximum && new.Minimum == existing.Minimum && new.ExclusiveMaximum == existing.ExclusiveMaximum && new.ExclusiveMinimum == existing.ExclusiveMinimum) {
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
		return field.ErrorList { field.InternalError(fldPath, errors.New("lcd argument should be passed when narrowExisting is true")) }
	}
	switch existing.Type {
		case "number": return lcdForNumber(fldPath, existing, new, lcd, narrowExisting)
		case "integer": return lcdForInteger(fldPath, existing, new, lcd, narrowExisting)
		case "string": return lcdForString(fldPath, existing, new, lcd, narrowExisting)
		case "boolean": return lcdForBoolean(fldPath, existing, new, lcd, narrowExisting)
		case "array": return lcdForArray(fldPath, existing, new, lcd, narrowExisting)
		case "object": return lcdForObject(fldPath, existing, new, lcd, narrowExisting)
		case "": if existing.XIntOrString {
			return lcdForIntOrString(fldPath, existing, new, lcd, narrowExisting)
		} else if existing.XPreserveUnknownFields {
			return lcdForPreserveUnknownFields(fldPath, existing, new, lcd, narrowExisting)
		}
	}
	return field.ErrorList { field.Invalid(field.NewPath(fldPath.String(), "type"), existing, "Invalid type") }
}

func lcdForIntegerValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidationForNumerics(fldPath, existing, new, "integer")...)
	return
}

func lcdForNumberValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation) (errorList field.ErrorList) {
	errorList = append(errorList, checkUnsupportedValidationForNumerics(fldPath, existing, new, "numbers")...)
	return
}

func lcdForNumber(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	if new.Type == "integer" {
		// new type is a subset of the existing type.
		if ! narrowExisting {
			errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
			return
		}
		lcd.Type = new.Type
		errorList = append(errorList, lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation)...)
		return
	}

	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	if len (errorList) > 0 {
		return
	}
	
	errorList = append(errorList, lcdForNumberValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation)...)
	return 
}

func lcdForInteger(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	if new.Type == "number" {
		// new type is a superset of the existing type.
		// all is well type-wise
		// keep the existing type (integer) in the LCD
	} else {
		errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
		if len (errorList) > 0 {
			return
		}
	}
	errorList = append(errorList, lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation)...)
	return 
}

func lcdForStringValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) (errorList field.ErrorList) {
	if ! (new.MaxLength == existing.MaxLength && new.MinLength == existing.MinLength) {
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
			if ! isString {
				errorList = append(errorList, field.Invalid(fldPath.Child("enum"), enum, "enum value should be a 'string' for Json type 'string'"))
				continue
			}
			enumSet.Insert(strVal)
		}
		return enumSet
	} 
	existingEnumValues := toEnumSets(existing.Enum)
	newEnumValues := toEnumSets(new.Enum)
	if ! newEnumValues.IsSuperset(existingEnumValues) {
		if narrowExisting {
			lcd.Enum = nil
			lcdEnumValues := existingEnumValues.Intersection(newEnumValues).List()
			for _, val := range lcdEnumValues {
				lcd.Enum = append(lcd.Enum, schema.JSON{ Object: val })
			}
		}
		errorList = append(errorList, field.Invalid(fldPath.Child("enum"), new.Enum, "enum value has been changed in an incompatible way"))		
	} else {
		// new enum values is a superset of existing enum values, so let the LCD keep the existing enum values  
	}
	return
}

func lcdForString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	errorList = append(errorList, lcdForStringValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)...)
	return 
}

func lcdForBoolean(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	lcd = existing.DeepCopy()
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)

	// manage enums ?
	return 
}

func lcdForArray(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	lcd = existing.DeepCopy()
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)
	var lcdErrorList field.ErrorList
	lcdErrorList = lcdForStructural(fldPath.Child("Items"), existing.Items, new.Items, lcd.Items, narrowExisting)
	errorList = append(errorList, lcdErrorList...)

	// manage additional validations (maxItems, minItems, etc...)
	return 
}

func lcdForObject(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	lcd = existing.DeepCopy()
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)

	// manage everything :-)
	return 
}

func lcdForIntOrString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	lcd = existing.DeepCopy()
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)

	// check equality of the extensions
	return 
}

func lcdForPreserveUnknownFields(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) (errorList field.ErrorList) {
	lcd = existing.DeepCopy()
	errorList = append(errorList, checkTypesAreTheSame(fldPath, existing, new)...)

	// check equality of the extensions, and possibly the equivalence of a embeddedResource extension
	// if it is there
	return 
}
