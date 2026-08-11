package main

import (
	"reflect"
	"strings"
	"time"

	"platform/sdk/contracts"
)

var timeType = reflect.TypeOf(time.Time{})

// enums maps a named string type to its allowed values, so the schema for a
// field of that type carries an enum. Kept explicit because Go const groups are
// not reachable by reflection.
var enums = map[string][]string{
	"ServiceType": {"ui", "api", "hybrid"},
}

// manifestSchema returns the JSON Schema (draft-07) for the manifest a plugin
// ships. It is generated from the Go struct, so it cannot drift, and it doubles
// as the runtime validator.
func manifestSchema() map[string]any {
	s := structSchema(reflect.TypeOf(contracts.ServiceManifest{}))
	s["$schema"] = "http://json-schema.org/draft-07/schema#"
	s["title"] = "ServiceManifest"
	s["description"] = "The manifest a plugin/service ships to the platform. Backend is the source of truth; the frontend reads it."
	return s
}

func schemaFor(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return schemaFor(t.Elem())
	case reflect.String:
		s := map[string]any{"type": "string"}
		if vals, ok := enums[t.Name()]; ok {
			s["enum"] = vals
		}
		return s
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem())}
	case reflect.Interface:
		return map[string]any{} // any
	case reflect.Struct:
		if t == timeType {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		return structSchema(t)
	default:
		return map[string]any{}
	}
}

func structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		jsonTag := f.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}
		name := f.Name
		if jsonTag != "" {
			if n := strings.Split(jsonTag, ",")[0]; n != "" {
				name = n
			}
		}
		props[name] = schemaFor(f.Type)
		if v, ok := f.Tag.Lookup("jsonschema"); ok {
			for _, part := range strings.Split(v, ",") {
				if strings.TrimSpace(part) == "required" {
					required = append(required, name)
				}
			}
		}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
