package mantyx

import (
	"context"
	"encoding/json"
)

// ToolRef is the developer-facing tool reference type. Use one of:
//
//   - MantyxTool(id)                — workspace tool by id
//   - MantyxPluginTool(name)        — plugin tool by `@plugin/tool` name
//   - LocalTool(LocalToolSpec{...}) — handler running in this process
type ToolRef interface {
	toolWire() map[string]any
}

type mantyxToolRef struct{ id string }

func (r mantyxToolRef) toolWire() map[string]any {
	return map[string]any{"kind": "mantyx", "id": r.id}
}

// MantyxTool references an existing workspace `Tool` row by id.
func MantyxTool(id string) ToolRef { return mantyxToolRef{id: id} }

type mantyxPluginToolRef struct{ name string }

func (r mantyxPluginToolRef) toolWire() map[string]any {
	return map[string]any{"kind": "mantyx_plugin", "name": r.name}
}

// MantyxPluginTool references a plugin tool by its `@plugin-slug/tool-name`.
func MantyxPluginTool(name string) ToolRef { return mantyxPluginToolRef{name: name} }

// LocalToolSpec describes a tool that runs in the developer's process.
type LocalToolSpec struct {
	// Name must match /^[a-zA-Z0-9_]{1,64}$/.
	Name string
	// Description is shown to the LLM as the tool's purpose.
	Description string
	// Parameters is one of:
	//   - nil                                 → empty object schema
	//   - map[string]any / json.RawMessage     → passed through as-is
	//   - a Go struct (or pointer-to-struct)   → reflected to JSON Schema
	Parameters any
	// Execute is invoked when the LLM calls this tool. The runtime delivers the
	// tool's arguments as raw JSON; the function returns the tool result as a
	// string (any non-string can be returned via json.Marshal yourself).
	Execute func(ctx context.Context, args json.RawMessage) (string, error)
}

type localTool struct {
	spec   LocalToolSpec
	schema map[string]any
}

func (t *localTool) toolWire() map[string]any {
	return map[string]any{
		"kind":        "local",
		"name":        t.spec.Name,
		"description": t.spec.Description,
		"parameters":  t.schema,
	}
}

// LocalTool registers a local tool. `Execute` runs in the SDK process whenever
// the agent loop emits a `local_tool_call` event for this tool's name.
func LocalTool(spec LocalToolSpec) ToolRef {
	schema, err := jsonSchemaFor(spec.Parameters)
	if err != nil {
		// Fall back to permissive object schema; surface as best-effort.
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &localTool{spec: spec, schema: schema}
}

// localToolRegistry maps tool name to the LocalTool that registered it. Used
// by the run driver to dispatch `local_tool_call` events.
type localToolRegistry map[string]*localTool

func collectLocalHandlers(tools []ToolRef) localToolRegistry {
	out := localToolRegistry{}
	for _, t := range tools {
		if lt, ok := t.(*localTool); ok {
			out[lt.spec.Name] = lt
		}
	}
	return out
}

func toolWire(tools []ToolRef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.toolWire())
	}
	return out
}
