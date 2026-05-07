package mantyx

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestJSONSchemaFor_PassThroughs covers the non-reflective inputs.
func TestJSONSchemaFor_PassThroughs(t *testing.T) {
	t.Run("nil yields empty object schema", func(t *testing.T) {
		got, err := jsonSchemaFor(nil)
		if err != nil {
			t.Fatalf("jsonSchemaFor(nil): %v", err)
		}
		if got["type"] != "object" {
			t.Fatalf("expected type=object, got %#v", got)
		}
		props, _ := got["properties"].(map[string]any)
		if props == nil || len(props) != 0 {
			t.Fatalf("expected empty properties, got %#v", got["properties"])
		}
	})

	t.Run("map[string]any returned as-is", func(t *testing.T) {
		in := map[string]any{
			"type":     "object",
			"required": []any{"x"},
		}
		got, err := jsonSchemaFor(in)
		if err != nil {
			t.Fatalf("jsonSchemaFor(map): %v", err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("expected pass-through, got %#v", got)
		}
	})

	t.Run("json.RawMessage is decoded", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
		got, err := jsonSchemaFor(raw)
		if err != nil {
			t.Fatalf("jsonSchemaFor(raw): %v", err)
		}
		if got["type"] != "object" {
			t.Fatalf("expected type=object, got %#v", got)
		}
	})
}

// TestJSONSchemaFor_StructReflection covers the happy-path reflection: a
// struct's exported fields with `json` and `jsonschema` tags become a
// JSON-Schema object with required entries and per-field descriptions.
func TestJSONSchemaFor_StructReflection(t *testing.T) {
	type readFileArgs struct {
		Path     string `json:"path" jsonschema:"Path to read"`
		Encoding string `json:"encoding,omitempty"`
	}

	got, err := jsonSchemaFor(&readFileArgs{})
	if err != nil {
		t.Fatalf("jsonSchemaFor(*struct): %v", err)
	}

	if got["type"] != "object" {
		t.Fatalf("expected root type=object, got %#v", got["type"])
	}
	if _, has := got["additionalProperties"]; has {
		t.Fatalf("additionalProperties should be stripped, got %#v", got["additionalProperties"])
	}
	if _, has := got["$schema"]; has {
		t.Fatalf("$schema should be stripped")
	}
	props, _ := got["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("missing properties: %#v", got)
	}
	pathSchema, _ := props["path"].(map[string]any)
	if pathSchema == nil {
		t.Fatalf("missing path property: %#v", props)
	}
	if pathSchema["type"] != "string" {
		t.Fatalf("expected path.type=string, got %#v", pathSchema["type"])
	}
	if pathSchema["description"] != "Path to read" {
		t.Fatalf("expected path.description=%q, got %#v", "Path to read", pathSchema["description"])
	}
	required, _ := got["required"].([]any)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("expected required=[path] (encoding has omitempty), got %#v", required)
	}
}

// TestJSONSchemaFor_CollapsesNullableUnions verifies that fields the
// upstream library decorates with `["null", X]` (nilable Go slices and
// pointer fields) collapse to the concrete type — what the LLM actually
// sees as a tool-parameter schema.
func TestJSONSchemaFor_CollapsesNullableUnions(t *testing.T) {
	type rangeArgs struct {
		Range []int   `json:"range" jsonschema:"Inclusive integer range"`
		Note  *string `json:"note,omitempty"`
	}

	got, err := jsonSchemaFor(&rangeArgs{})
	if err != nil {
		t.Fatalf("jsonSchemaFor: %v", err)
	}
	props, _ := got["properties"].(map[string]any)
	rangeSchema, _ := props["range"].(map[string]any)
	if rangeSchema == nil {
		t.Fatalf("missing range property: %#v", props)
	}
	if rangeSchema["type"] != "array" {
		t.Fatalf("expected range.type=array (collapsed from [null,array]), got %#v", rangeSchema["type"])
	}
	items, _ := rangeSchema["items"].(map[string]any)
	if items == nil || items["type"] != "integer" {
		t.Fatalf("expected range.items.type=integer, got %#v", rangeSchema["items"])
	}

	noteSchema, _ := props["note"].(map[string]any)
	if noteSchema == nil {
		t.Fatalf("missing note property: %#v", props)
	}
	if noteSchema["type"] != "string" {
		t.Fatalf("expected note.type=string (collapsed from [null,string]), got %#v", noteSchema["type"])
	}
}
