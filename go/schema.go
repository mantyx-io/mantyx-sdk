package mantyx

import (
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
)

// jsonSchemaFor turns a Go value (typically a pointer to a struct) into a
// JSON-Schema-shaped map suitable for the local-tool definition payload.
//
// Accepted inputs:
//   - nil                              → empty object schema
//   - map[string]any / json.RawMessage → returned as-is (already JSON-Schema)
//   - any other Go value               → reflected via google/jsonschema-go
//
// Struct fields use the `jsonschema:"..."` tag for the property `description`.
// The tag value MUST NOT begin with `WORD=` (reserved for future expansion by
// the upstream library).
func jsonSchemaFor(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case json.RawMessage:
		var out map[string]any
		if err := json.Unmarshal(x, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	t := reflect.TypeOf(v)
	// Deref any number of pointer levels so the root schema is e.g. "object"
	// rather than `["null", "object"]` for a `*MyArgs` input.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	schema, err := jsonschema.ForType(t, &jsonschema.ForOptions{
		IgnoreInvalidTypes: true,
	})
	if err != nil {
		return nil, err
	}
	return schemaToMap(schema)
}

// schemaToMap renders a *jsonschema.Schema into a wire-friendly
// map[string]any, dropping noisy keywords (`$schema`, `$id`,
// `additionalProperties`) and collapsing the upstream library's
// `type: ["null", X]` decoration on slices and pointer fields back to the
// concrete type — what the LLM consumes is a tool-parameter schema, where
// nullability is described by the absence of the field in `required`, not
// by JSON-Schema unions.
func schemaToMap(s *jsonschema.Schema) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	cleanSchemaNode(out)
	return out, nil
}

// cleanSchemaNode walks the decoded schema in place, stripping noisy
// keywords and collapsing `type: ["null", X]` unions so tool-parameter
// schemas stay compact and LLM-friendly.
func cleanSchemaNode(node map[string]any) {
	delete(node, "$schema")
	delete(node, "$id")
	delete(node, "additionalProperties")

	if types, ok := node["type"].([]any); ok && len(types) == 2 {
		var nonNull any
		nullSeen := false
		for _, t := range types {
			s, ok := t.(string)
			if !ok {
				continue
			}
			if s == "null" {
				nullSeen = true
				continue
			}
			nonNull = s
		}
		if nullSeen && nonNull != nil {
			node["type"] = nonNull
		}
	}

	for _, v := range node {
		switch vv := v.(type) {
		case map[string]any:
			cleanSchemaNode(vv)
		case []any:
			for _, item := range vv {
				if m, ok := item.(map[string]any); ok {
					cleanSchemaNode(m)
				}
			}
		}
	}
}
