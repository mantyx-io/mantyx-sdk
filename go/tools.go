package mantyx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

// ToolRef is the developer-facing tool reference type. Use one of:
//
// Server-resolved (MANTYX runs the tool itself):
//   - MantyxTool(id)                — workspace tool by id
//   - MantyxPluginTool(name)        — plugin tool by `@plugin/tool` name
//   - MantyxA2A(MantyxA2AOptions{}) — remote Agent2Agent peer
//   - MantyxMcp(MantyxMcpOptions{}) — remote MCP server (Streamable HTTP)
//
// Client-resolved (the SDK runs the tool in this process):
//   - LocalTool(LocalToolSpec{}) — generic local tool
//   - LocalA2A(LocalA2ASpec{})   — A2A peer addressed by URL; SDK fetches the
//     Agent Card and dials the peer transparently.
//   - LocalMcp(LocalMcpSpec{})   — MCP server addressed by URL or stdio
//     command; SDK manages the transport, discovery, and tool calls
//     transparently using the official MCP Go SDK.
type ToolRef interface {
	toolWire() map[string]any
}

var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

// ----- mantyx (workspace tool by id) ---------------------------------------

type mantyxToolRef struct{ id string }

func (r mantyxToolRef) toolWire() map[string]any {
	return map[string]any{"kind": "mantyx", "id": r.id}
}

// MantyxTool references an existing workspace `Tool` row by id.
func MantyxTool(id string) ToolRef { return mantyxToolRef{id: id} }

// ----- mantyx_plugin (platform plugin tool by name) ------------------------

type mantyxPluginToolRef struct{ name string }

func (r mantyxPluginToolRef) toolWire() map[string]any {
	return map[string]any{"kind": "mantyx_plugin", "name": r.name}
}

// MantyxPluginTool references a plugin tool by its `@plugin-slug/tool-name`.
func MantyxPluginTool(name string) ToolRef { return mantyxPluginToolRef{name: name} }

// ----- local (generic local tool) ------------------------------------------

// LocalToolSpec describes a tool that runs in the developer's process.
type LocalToolSpec struct {
	// Name must match /^[a-zA-Z0-9_]{1,64}$/.
	Name string
	// Description is shown to the LLM as the tool's purpose.
	Description string
	// Parameters is one of:
	//   - nil                                  → empty object schema
	//   - map[string]any / json.RawMessage     → passed through as-is
	//   - a Go struct (or pointer-to-struct)   → reflected to JSON Schema
	Parameters any
	// Execute is invoked when the LLM calls this tool. It must be a function
	// with the signature:
	//
	//	func(ctx context.Context, args T) (R, error)
	//
	// where T is the same Go type used for Parameters (or its pointer/value
	// counterpart). The SDK json.Unmarshals the raw tool arguments into a
	// fresh value of T before calling Execute. T may also be json.RawMessage,
	// in which case the SDK forwards the unparsed JSON bytes verbatim.
	//
	// R is the result type the SDK serialises to the wire as a string:
	//
	//   - string           → forwarded verbatim (legacy contract).
	//   - json.RawMessage  → forwarded verbatim (raw JSON bytes).
	//   - any other type   → SDK json.Marshals the value, returning the
	//     resulting JSON text. This pairs naturally with Parameters: a
	//     single Go return type drives both the typed handler return and
	//     the JSON the model receives.
	//
	// A non-nil error short-circuits encoding and is forwarded to the model
	// as a tool-error response.
	Execute any
}

type localTool struct {
	spec   LocalToolSpec
	schema map[string]any
	invoke func(ctx context.Context, raw json.RawMessage) (string, error)
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
	if !toolNameRe.MatchString(spec.Name) {
		panic(fmt.Sprintf("mantyx.LocalTool: invalid tool name %q (must match /^[a-zA-Z0-9_]{1,64}$/)", spec.Name))
	}
	schema, err := jsonSchemaFor(spec.Parameters)
	if err != nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	invoke, err := buildLocalToolInvoker(spec.Name, spec.Execute)
	if err != nil {
		panic(fmt.Sprintf("mantyx.LocalTool %q: %v", spec.Name, err))
	}
	return &localTool{spec: spec, schema: schema, invoke: invoke}
}

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	rawMsgType  = reflect.TypeOf(json.RawMessage(nil))
)

// buildLocalToolInvoker validates spec.Execute's shape and returns a closure
// that decodes the raw JSON args into Execute's typed parameter, invokes the
// function, and returns its result as the wire-level (string, error) reply.
//
// Accepted Execute signatures:
//
//	func(ctx context.Context, args T) (R, error)
//
// where T is any concrete Go type (struct, *struct, map[string]any,
// json.RawMessage, primitives, …). For json.RawMessage the SDK forwards the
// raw bytes verbatim; otherwise it allocates a zero-valued T (or *T's
// pointee) and json.Unmarshals the raw args into it before the call.
//
// R may be string (forwarded as-is), json.RawMessage (forwarded as-is), or
// any other Go type (json.Marshaled by the SDK before dispatch). Pairing R
// with Parameters keeps both ends of the contract type-checked by the Go
// compiler.
func buildLocalToolInvoker(toolName string, execute any) (func(ctx context.Context, raw json.RawMessage) (string, error), error) {
	if execute == nil {
		return nil, fmt.Errorf("Execute is required")
	}
	// Fast path: legacy json.RawMessage-in / string-out signature avoids
	// reflection cost on every dispatch.
	if fn, ok := execute.(func(context.Context, json.RawMessage) (string, error)); ok {
		return fn, nil
	}

	rv := reflect.ValueOf(execute)
	rt := rv.Type()
	if rt.Kind() != reflect.Func {
		return nil, fmt.Errorf("Execute must be a function, got %s", rt.Kind())
	}
	if rt.NumIn() != 2 || rt.NumOut() != 2 {
		return nil, fmt.Errorf("Execute must have signature func(context.Context, T) (R, error)")
	}
	if !rt.In(0).Implements(contextType) {
		return nil, fmt.Errorf("Execute first parameter must be context.Context, got %s", rt.In(0))
	}
	if !rt.Out(1).Implements(errorType) {
		return nil, fmt.Errorf("Execute second return must be error, got %s", rt.Out(1))
	}
	encoder := resultEncoderFor(rt.Out(0))

	argType := rt.In(1)
	isRawMessage := argType == rawMsgType
	isPointer := argType.Kind() == reflect.Ptr

	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		// Allocate a fresh *T (or *Telem when T is a pointer) so we have a
		// settable target for json.Unmarshal.
		var holder reflect.Value
		if isPointer {
			holder = reflect.New(argType.Elem())
		} else {
			holder = reflect.New(argType)
		}
		if !isRawMessage && len(raw) > 0 {
			if err := json.Unmarshal(raw, holder.Interface()); err != nil {
				return "", fmt.Errorf("decode args for tool %q: %w", toolName, err)
			}
		} else if isRawMessage {
			// Stash raw bytes directly on the json.RawMessage holder.
			holder.Elem().SetBytes([]byte(raw))
		}

		var argVal reflect.Value
		if isPointer {
			argVal = holder
		} else {
			argVal = holder.Elem()
		}
		out := rv.Call([]reflect.Value{reflect.ValueOf(ctx), argVal})
		var rerr error
		if v := out[1].Interface(); v != nil {
			rerr, _ = v.(error)
		}
		if rerr != nil {
			// Skip result encoding on error — the caller drops the result
			// payload and forwards err.Error() as the tool-error response.
			return "", rerr
		}
		result, encErr := encoder(out[0])
		if encErr != nil {
			return "", fmt.Errorf("encode result for tool %q: %w", toolName, encErr)
		}
		return result, nil
	}, nil
}

// resultEncoderFor returns a function that converts Execute's first return
// value into the wire-level string the SDK posts back as a tool result.
//
//   - string          → returned verbatim.
//   - json.RawMessage → returned verbatim (raw JSON bytes).
//   - everything else → json.Marshaled to JSON text.
//
// The tail case allows handlers to declare typed result structs (mirroring
// how Parameters lets them declare typed argument structs) and have the
// SDK do the marshaling on their behalf. Marshal failures are surfaced
// from the call site as `encode result for tool …` errors at dispatch time.
func resultEncoderFor(rt reflect.Type) func(reflect.Value) (string, error) {
	switch {
	case rt.Kind() == reflect.String:
		return func(v reflect.Value) (string, error) { return v.String(), nil }
	case rt == rawMsgType:
		return func(v reflect.Value) (string, error) {
			// json.RawMessage is []byte under the hood — forward the
			// raw JSON text without re-marshaling.
			return string(v.Bytes()), nil
		}
	default:
		return func(v reflect.Value) (string, error) {
			b, err := json.Marshal(v.Interface())
			if err != nil {
				return "", err
			}
			return string(b), nil
		}
	}
}

// ----- a2a (server-resolved Agent2Agent) -----------------------------------

// MantyxA2AOptions describes a remote Agent2Agent peer reachable from MANTYX.
type MantyxA2AOptions struct {
	// Name surfaced to the model; must match /^[a-zA-Z0-9_]{1,64}$/.
	Name string
	// Description shown to the model. Falls back to a generic delegation hint.
	Description string
	// AgentCardURL is the remote Agent Card URL or JSON-RPC root.
	AgentCardURL string
	// Headers are forwarded as-is on every A2A request (typically Authorization).
	Headers map[string]string
	// ContextID, when set, threads multiple delegations into the same A2A
	// remote conversation. Omit for fresh per-call context.
	ContextID string
}

type a2aToolRef struct{ opts MantyxA2AOptions }

func (r a2aToolRef) toolWire() map[string]any {
	out := map[string]any{
		"kind":         "a2a",
		"name":         r.opts.Name,
		"agentCardUrl": r.opts.AgentCardURL,
	}
	if r.opts.Description != "" {
		out["description"] = r.opts.Description
	}
	if len(r.opts.Headers) > 0 {
		headers := make(map[string]any, len(r.opts.Headers))
		for k, v := range r.opts.Headers {
			headers[k] = v
		}
		out["headers"] = headers
	}
	if r.opts.ContextID != "" {
		out["contextId"] = r.opts.ContextID
	}
	return out
}

// MantyxA2A registers a remote Agent2Agent peer. MANTYX dials the peer over
// A2A's `message/send` RPC and forwards the reply as the tool result.
func MantyxA2A(opts MantyxA2AOptions) ToolRef {
	if !toolNameRe.MatchString(opts.Name) {
		panic(fmt.Sprintf("mantyx.MantyxA2A: invalid tool name %q", opts.Name))
	}
	if opts.AgentCardURL == "" {
		panic("mantyx.MantyxA2A: AgentCardURL is required")
	}
	return a2aToolRef{opts: opts}
}

// ----- a2a_local (client-resolved Agent2Agent) -----------------------------

// LocalA2ASpec describes an Agent2Agent peer the SDK reaches on MANTYX's
// behalf. You only supply the Agent Card URL — the SDK fetches the Agent
// Card on the first run, ships it inline with the spec (so MANTYX never
// dials your peer directly), and JSON-RPC `message/send`s the model's
// `message` argument against the resolved card's `url` whenever MANTYX
// emits a `local_tool_call` event for this tool.
//
// Per `docs/wire-protocol.md` §3.1, MANTYX uses the resolved card's
// `name`, `description`, and the first 12 `skills` to compose the
// model-facing tool description.
type LocalA2ASpec struct {
	// Name surfaced to the model; must match /^[a-zA-Z0-9_]{1,64}$/.
	Name string
	// Description is an optional model-facing description override. When
	// empty, MANTYX synthesizes one from the resolved Agent Card.
	Description string
	// AgentCardURL is the location of the peer's A2A Agent Card. Required.
	// Typical shape: `https://hr.intranet.acme/.well-known/agent-card.json`.
	AgentCardURL string
	// Headers are forwarded as-is on both the Agent Card GET and the
	// JSON-RPC `message/send` POST (typically `Authorization: Bearer ...`).
	Headers map[string]string
	// HTTPClient overrides the default `http.DefaultClient` used to fetch
	// the Agent Card and dial the peer's `message/send` endpoint.
	HTTPClient *http.Client
}

// localA2ATool is the internal `kind: "a2a_local"` ToolRef. It caches the
// resolved Agent Card after the first fetch so subsequent runs and
// `local_tool_call` dispatches don't refetch.
type localA2ATool struct {
	spec LocalA2ASpec

	mu           sync.Mutex
	resolvedCard map[string]any // raw JSON object as fetched, with at least "name"
}

func (t *localA2ATool) toolWire() map[string]any {
	t.mu.Lock()
	card := t.resolvedCard
	t.mu.Unlock()
	if card == nil {
		// Resolution should always run before serialization. Falling back to
		// a stub avoids a hard panic while still surfacing a missing-name
		// error server-side.
		card = map[string]any{"name": t.spec.Name}
	}
	out := map[string]any{
		"kind":      "a2a_local",
		"name":      t.spec.Name,
		"agentCard": card,
	}
	if t.spec.Description != "" {
		out["description"] = t.spec.Description
	}
	return out
}

// LocalA2A registers an Agent2Agent peer the SDK reaches on MANTYX's behalf.
// The SDK fetches the Agent Card from spec.AgentCardURL on the first run /
// session.send, caches the result, and dials the peer's JSON-RPC `message/send`
// endpoint for every `local_tool_call` event MANTYX emits.
func LocalA2A(spec LocalA2ASpec) ToolRef {
	if !toolNameRe.MatchString(spec.Name) {
		panic(fmt.Sprintf("mantyx.LocalA2A: invalid tool name %q", spec.Name))
	}
	if spec.AgentCardURL == "" {
		panic("mantyx.LocalA2A: AgentCardURL is required")
	}
	return &localA2ATool{spec: spec}
}

// ----- mcp (server-resolved MCP server) ------------------------------------

// MantyxMcpOptions describes a remote MCP server (Streamable HTTP) discovered
// and proxied by MANTYX. Each tool in the catalog surfaces as `<name>_<tool>`.
type MantyxMcpOptions struct {
	// Name is the server label; must match /^[a-zA-Z0-9_]{1,64}$/.
	Name string
	// URL is the Streamable HTTP MCP endpoint.
	URL string
	// Headers are forwarded as-is on every MCP request.
	Headers map[string]string
	// ToolFilter, when non-empty, allows only the listed MCP tool names.
	ToolFilter []string
}

type mcpToolRef struct{ opts MantyxMcpOptions }

func (r mcpToolRef) toolWire() map[string]any {
	out := map[string]any{
		"kind": "mcp",
		"name": r.opts.Name,
		"url":  r.opts.URL,
	}
	if len(r.opts.Headers) > 0 {
		headers := make(map[string]any, len(r.opts.Headers))
		for k, v := range r.opts.Headers {
			headers[k] = v
		}
		out["headers"] = headers
	}
	if len(r.opts.ToolFilter) > 0 {
		filter := make([]any, len(r.opts.ToolFilter))
		for i, s := range r.opts.ToolFilter {
			filter[i] = s
		}
		out["toolFilter"] = filter
	}
	return out
}

// MantyxMcp registers a remote MCP server reachable from MANTYX.
func MantyxMcp(opts MantyxMcpOptions) ToolRef {
	if !toolNameRe.MatchString(opts.Name) {
		panic(fmt.Sprintf("mantyx.MantyxMcp: invalid server name %q", opts.Name))
	}
	if opts.URL == "" {
		panic("mantyx.MantyxMcp: URL is required")
	}
	return mcpToolRef{opts: opts}
}

// ----- mcp_local (client-resolved MCP server) ------------------------------

// LocalMcpSpec describes an MCP server the SDK manages end-to-end. Provide
// either a Streamable HTTP transport (URL + optional Headers) **or** an
// stdio transport (Command + Args/Env/Cwd) — never both. The SDK opens the
// transport on the first run / session, runs `Initialize` + `tools/list` to
// discover the catalog, ships the resolved catalog inline with the agent
// spec (so MANTYX can render the tools to the model under the
// `<server>_<tool>` naming convention), and forwards every
// `local_tool_call` event MANTYX emits to the live MCP session via
// `tools/call`. The transport closes when the run / session ends.
//
// Internally the SDK uses the official
// `github.com/modelcontextprotocol/go-sdk/mcp` package.
type LocalMcpSpec struct {
	// Name is the server label echoed back as `mcpServer` on every
	// `local_tool_call`. Must match /^[a-zA-Z0-9_]{1,64}$/. The SDK
	// auto-prefixes each tool's wire-level `name` with `<this>_` so the
	// model sees a non-colliding `<server>_<tool>` surface.
	Name string

	// HTTP transport (mutually exclusive with stdio).

	// URL is the Streamable HTTP MCP endpoint.
	URL string
	// Headers are forwarded as-is on every HTTP request to the server.
	Headers map[string]string
	// HTTPClient overrides the default `http.DefaultClient` used by the
	// Streamable HTTP transport. Ignored for stdio.
	HTTPClient *http.Client

	// stdio transport (mutually exclusive with HTTP).

	// Command is the binary to launch (e.g. `mcp-server-filesystem`).
	Command string
	// Args are the command-line arguments passed to Command.
	Args []string
	// Env is appended to the child process's environment, on top of os.Environ().
	Env map[string]string
	// Cwd is the working directory of the child process. Empty inherits.
	Cwd string
}

// localMcpServer is the internal `kind: "mcp_local"` ToolRef. It owns the
// MCP transport / session lifetime and caches the resolved tool catalog
// for both wire serialization and `local_tool_call` dispatch.
type localMcpServer struct {
	spec LocalMcpSpec

	mu       sync.Mutex
	resolved *resolvedMcp
}

// resolvedMcp captures the post-Initialize state of an `mcp_local` server.
// It is populated by the resolver before the first run starts and reused
// by both wire serialization and `local_tool_call` dispatch. The function
// fields are kept on the struct so tests can drop in a fake resolution
// without spawning a real MCP transport.
type resolvedMcp struct {
	// serverInfo is the wire-friendly `Implementation` block from MCP
	// `Initialize` (typically `{name, version, ...}`). May be nil.
	serverInfo map[string]any
	// tools is the wire-ready catalog: each entry already has its `name`
	// auto-prefixed with the server's Name and matches the verbatim MCP
	// `tools/list` shape (`name`, `description`, `inputSchema`,
	// `annotations?`, ...).
	tools []map[string]any
	// upstreamNames maps the wire-prefixed name (e.g. `fs_read_file`) back
	// to the upstream MCP tool name (e.g. `read_file`) used on the
	// `tools/call` invocation.
	upstreamNames map[string]string

	// callTool is invoked for every dispatched `local_tool_call`. The
	// caller passes the upstream MCP tool name and raw JSON args; the
	// callee performs the `tools/call` RPC and returns the flattened text
	// reply.
	callTool func(ctx context.Context, toolName string, args json.RawMessage) (string, error)
	// close releases any underlying transport (HTTP keep-alive,
	// subprocess, ...). Called by closeMcpRefs on session / run end.
	close func() error
}

func (s *localMcpServer) toolWire() map[string]any {
	s.mu.Lock()
	r := s.resolved
	s.mu.Unlock()
	out := map[string]any{
		"kind":  "mcp_local",
		"name":  s.spec.Name,
		"tools": []map[string]any{},
	}
	if r == nil {
		return out
	}
	if r.serverInfo != nil {
		out["serverInfo"] = r.serverInfo
	}
	out["tools"] = r.tools
	return out
}

// prefixedMcpToolName composes the wire-level (model-facing) tool name for a
// `mcp_local` entry. It prepends `<server>_` unless the tool name already
// starts with that prefix, so manual prefixing stays idempotent.
func prefixedMcpToolName(serverName, toolName string) string {
	prefix := serverName + "_"
	if strings.HasPrefix(toolName, prefix) {
		return toolName
	}
	return prefix + toolName
}

// LocalMcp registers a local MCP server with a transport-only spec. The SDK
// performs Initialize + tools/list on the first run / session.send and
// dispatches each `local_tool_call` event with `kind: "mcp_local"` into the
// live session via `tools/call`.
func LocalMcp(spec LocalMcpSpec) ToolRef {
	if !toolNameRe.MatchString(spec.Name) {
		panic(fmt.Sprintf("mantyx.LocalMcp: invalid server name %q", spec.Name))
	}
	httpSet := spec.URL != ""
	stdioSet := spec.Command != ""
	switch {
	case httpSet && stdioSet:
		panic("mantyx.LocalMcp: specify either URL (Streamable HTTP) or Command (stdio), not both")
	case !httpSet && !stdioSet:
		panic("mantyx.LocalMcp: one of URL (Streamable HTTP) or Command (stdio) is required")
	}
	return &localMcpServer{spec: spec}
}

// ----- registry ------------------------------------------------------------

// localToolRegistry maps tool name to the LocalTool that registered it. Used
// by the run driver to dispatch `local_tool_call` events.
type localToolRegistry struct {
	localTools map[string]*localTool
	a2aTools   map[string]*localA2ATool
	mcpServers map[string]*localMcpServer
}

func newRegistry() *localToolRegistry {
	return &localToolRegistry{
		localTools: map[string]*localTool{},
		a2aTools:   map[string]*localA2ATool{},
		mcpServers: map[string]*localMcpServer{},
	}
}

func collectLocalHandlers(tools []ToolRef) *localToolRegistry {
	out := newRegistry()
	for _, t := range tools {
		switch h := t.(type) {
		case *localTool:
			out.localTools[h.spec.Name] = h
		case *localA2ATool:
			out.a2aTools[h.spec.Name] = h
		case *localMcpServer:
			out.mcpServers[h.spec.Name] = h
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
