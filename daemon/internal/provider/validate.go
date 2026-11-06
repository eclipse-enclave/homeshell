package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// ValidationError reports that a workstream config failed schema validation.
// Fields maps each offending field (a JSON pointer such as "/branch" or "/name")
// to a human-readable reason, so the API can return field-level detail.
// Err, when set, is the rule the refusal enforces, for errors.Is.
type ValidationError struct {
	Message string
	Fields  map[string]string
	Err     error
}

func (e *ValidationError) Error() string { return e.Message }

func (e *ValidationError) Unwrap() error { return e.Err }

// ValidateConfig checks config against a provider's workstream JSON Schema.
// An empty schema accepts any config. The service validates a create against
// the provider's schema (see validationSchema).
func ValidateConfig(schema, config json.RawMessage) error {
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return &ValidationError{Message: "workstream type has an invalid schema"}
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://workstream-type", schemaDoc); err != nil {
		return &ValidationError{Message: "workstream type has an invalid schema"}
	}
	sch, err := c.Compile("mem://workstream-type")
	if err != nil {
		return &ValidationError{Message: "workstream type has an invalid schema"}
	}

	var value any = map[string]any{}
	if len(bytes.TrimSpace(config)) != 0 {
		value, err = jsonschema.UnmarshalJSON(bytes.NewReader(config))
		if err != nil {
			return &ValidationError{Message: "configuration is not valid JSON"}
		}
	}

	err = sch.Validate(value)
	if err == nil {
		return nil
	}
	ve := &ValidationError{Message: "configuration is invalid", Fields: map[string]string{}}
	var verr *jsonschema.ValidationError
	if errors.As(err, &verr) {
		collectFieldErrors(verr.BasicOutput(), ve.Fields)
	}
	if len(ve.Fields) == 0 {
		ve.Message = err.Error()
	}
	return ve
}

// collectFieldErrors walks a basic output tree and records the leaf errors
// keyed by instance location, giving one message per offending field. A
// missing property is recorded against the property itself rather than the
// object that lacks it, so it is the field that is blamed.
func collectFieldErrors(unit *jsonschema.OutputUnit, into map[string]string) {
	if unit == nil {
		return
	}
	if len(unit.Errors) == 0 {
		if unit.Error == nil {
			return
		}
		if required, ok := unit.Error.Kind.(*kind.Required); ok {
			for _, property := range required.Missing {
				into[unit.InstanceLocation+"/"+escapePointer(property)] = "required"
			}
			return
		}
		key := unit.InstanceLocation
		if key == "" {
			key = "/"
		}
		// A value that matches none of a oneOf's choices also fails each choice,
		// and those failures ("value must be 'devtools'") say less than the oneOf's.
		if into[key] == notOnOffer {
			return
		}
		into[key] = fieldReason(unit.Error)
		return
	}
	for i := range unit.Errors {
		collectFieldErrors(&unit.Errors[i], into)
	}
}

// fieldReason words a failed schema keyword for a person, as a phrase that
// follows the field's name. A keyword without a wording of its own keeps the
// validator's.
func fieldReason(failure *jsonschema.OutputError) string {
	switch keyword := failure.Kind.(type) {
	case *kind.MinLength:
		if keyword.Want == 1 {
			return "required"
		}
		return fmt.Sprintf("must be at least %d characters", keyword.Want)
	case *kind.MaxLength:
		return fmt.Sprintf("must be at most %d characters", keyword.Want)
	case *kind.Enum:
		options := make([]string, len(keyword.Want))
		for i, option := range keyword.Want {
			options[i] = fmt.Sprint(option)
		}
		return "must be one of " + strings.Join(options, ", ")
	case *kind.Type:
		types := make([]string, len(keyword.Want))
		for i, name := range keyword.Want {
			types[i] = typeNames[name]
			if types[i] == "" {
				types[i] = name
			}
		}
		return "must be " + strings.Join(types, " or ")
	case *kind.AdditionalProperties:
		return "unknown settings: " + strings.Join(keyword.Properties, ", ")
	case *kind.Pattern:
		return "is not in the expected form"
	case *kind.Minimum:
		return "must be at least " + keyword.Want.RatString()
	case *kind.Maximum:
		return "must be at most " + keyword.Want.RatString()
	case *kind.UniqueItems:
		return "lists the same entry twice"
	case *kind.OneOf:
		if keyword.Subschemas == nil {
			return notOnOffer
		}
	}
	return failure.String()
}

// notOnOffer is the reason given for a value that matches none of a oneOf's
// choices.
const notOnOffer = "is not one of the choices on offer"

// typeNames words the JSON types a schema asks for.
var typeNames = map[string]string{
	"string":  "text",
	"number":  "a number",
	"integer": "a whole number",
	"boolean": "true or false",
	"object":  "an object",
	"array":   "a list",
	"null":    "empty",
}

// escapePointer escapes a property name as a JSON pointer segment (RFC 6901).
func escapePointer(segment string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(segment)
}

// FieldErrors extracts the field-level detail from an error if it is a
// ValidationError, returning nil otherwise.
func FieldErrors(err error) map[string]string {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Fields
	}
	return nil
}
