package mantyx

import (
	"encoding/json"
	"reflect"

	"github.com/invopop/jsonschema"
)

// jsonSchemaFor turns a Go value (typically a pointer to a struct) into a
// JSON-Schema-shaped map suitable for the local-tool definition payload.
//
// Accepted inputs:
//   - nil                            → empty object schema
//   - map[string]any / json.RawMessage → returned as-is (already JSON-Schema)
//   - any other Go value             → reflected via invopop/jsonschema
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

	r := &jsonschema.Reflector{
		ExpandedStruct:            true,
		AllowAdditionalProperties: true,
		DoNotReference:            true,
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		// Reflect on the concrete element type.
		schema := r.ReflectFromType(rv.Type().Elem())
		return schemaToMap(schema)
	}
	schema := r.Reflect(v)
	return schemaToMap(schema)
}

func schemaToMap(s *jsonschema.Schema) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	// Strip noisy fields so the wire payload stays compact.
	delete(out, "$schema")
	delete(out, "$id")
	delete(out, "additionalProperties")
	return out, nil
}
