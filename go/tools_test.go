package mantyx

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

// errorsAs is a tiny shim around errors.As so we don't have to rename the
// existing `errors` field in struct literals (the new test file above
// wants both).
func errorsAs(err error, target any) bool { return stderrors.As(err, target) }

// seedMcpResolution short-circuits the MCP transport handshake for tests by
// dropping a hand-rolled `resolvedMcp` onto the LocalMcp tool ref. Tests use
// it to assert wire serialization and dispatch without spawning a real MCP
// server. Tools are expected as the verbatim MCP `tools/list` shape (the
// resolver auto-prefixes the wire-level name; here you supply the upstream
// tool name and the helper handles the prefixing).
func seedMcpResolution(
	t *testing.T,
	ref ToolRef,
	serverInfo map[string]any,
	upstreamTools []map[string]any,
	callTool func(ctx context.Context, name string, args json.RawMessage) (string, error),
) {
	t.Helper()
	srv, ok := ref.(*localMcpServer)
	if !ok {
		t.Fatalf("seedMcpResolution: not a *localMcpServer (%T)", ref)
	}
	wireTools := make([]map[string]any, 0, len(upstreamTools))
	upstream := make(map[string]string, len(upstreamTools))
	for _, raw := range upstreamTools {
		entry := map[string]any{}
		for k, v := range raw {
			entry[k] = v
		}
		name, _ := raw["name"].(string)
		entry["name"] = prefixedMcpToolName(srv.spec.Name, name)
		if _, has := entry["inputSchema"]; !has {
			entry["inputSchema"] = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		wireTools = append(wireTools, entry)
		upstream[entry["name"].(string)] = name
	}
	srv.mu.Lock()
	srv.resolved = &resolvedMcp{
		serverInfo:    serverInfo,
		tools:         wireTools,
		upstreamNames: upstream,
		callTool:      callTool,
		close:         func() error { return nil },
	}
	srv.mu.Unlock()
}

// ---------- Tool ref serialization -----------------------------------------

func TestToolRef_MantyxA2A_Wire(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := MantyxA2A(MantyxA2AOptions{
		Name:         "billing",
		Description:  "Talk to the Acme billing agent.",
		AgentCardURL: "https://billing.acme.com/.well-known/agent-card.json",
		Headers:      map[string]string{"Authorization": "Bearer abc"},
		ContextID:    "ctx_1",
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(body.Tools))
	}
	got := body.Tools[0]
	if got["kind"] != "a2a" || got["name"] != "billing" || got["agentCardUrl"] != "https://billing.acme.com/.well-known/agent-card.json" {
		t.Fatalf("unexpected wire shape: %#v", got)
	}
	if got["description"] != "Talk to the Acme billing agent." {
		t.Fatalf("description not forwarded: %#v", got)
	}
	headers, _ := got["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer abc" {
		t.Fatalf("headers not forwarded: %#v", headers)
	}
	if got["contextId"] != "ctx_1" {
		t.Fatalf("contextId not forwarded: %#v", got)
	}
}

func TestToolRef_MantyxMcp_Wire(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := MantyxMcp(MantyxMcpOptions{
		Name:       "github",
		URL:        "https://mcp.github.com/v1",
		Headers:    map[string]string{"Authorization": "Bearer pat"},
		ToolFilter: []string{"search_repos", "read_file"},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	got := body.Tools[0]
	if got["kind"] != "mcp" || got["name"] != "github" || got["url"] != "https://mcp.github.com/v1" {
		t.Fatalf("unexpected wire shape: %#v", got)
	}
	filter, ok := got["toolFilter"].([]any)
	if !ok || len(filter) != 2 {
		t.Fatalf("toolFilter not forwarded: %#v", got["toolFilter"])
	}
}

func TestToolRef_LocalA2A_Wire_ShipsResolvedCard(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	server.a2aAgentCard = map[string]any{
		"protocolVersion": "0.3.0",
		"name":            "Acme HR",
		"description":     "Answers HR questions.",
		"url":             server.baseURL() + "/a2a/rpc",
		"skills": []any{
			map[string]any{"id": "pto_lookup", "name": "PTO lookup"},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := LocalA2A(LocalA2ASpec{
		Name:         "intranet_hr",
		Description:  "Delegate HR questions.",
		AgentCardURL: server.baseURL() + "/a2a/agent-card.json",
		Headers:      map[string]string{"Authorization": "Bearer hr-token"},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if server.a2aAuthHeader != "Bearer hr-token" {
		t.Fatalf("auth header not forwarded on Agent Card fetch: %q", server.a2aAuthHeader)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	got := body.Tools[0]
	if got["kind"] != "a2a_local" || got["name"] != "intranet_hr" {
		t.Fatalf("unexpected wire shape: %#v", got)
	}
	card, ok := got["agentCard"].(map[string]any)
	if !ok {
		t.Fatalf("agentCard not forwarded as object: %#v", got)
	}
	if card["name"] != "Acme HR" || card["protocolVersion"] != "0.3.0" {
		t.Fatalf("agent card fields not preserved: %#v", card)
	}
	skills, ok := card["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("skills not forwarded: %#v", card["skills"])
	}
	if _, has := got["agentCardUrl"]; has {
		t.Fatalf("agentCardUrl leaked onto a2a_local wire (server-resolved field): %#v", got)
	}
	if _, has := got["headers"]; has {
		t.Fatalf("headers leaked onto a2a_local wire: %#v", got)
	}
}

func TestLocalA2A_PanicsWhenAgentCardURLMissing(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when AgentCardURL is empty")
		}
	}()
	LocalA2A(LocalA2ASpec{Name: "intranet_hr"})
}

func TestLocalA2A_RejectsCardMissingName(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	server.a2aAgentCard = map[string]any{"description": "no name"}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := LocalA2A(LocalA2ASpec{
		Name:         "intranet_hr",
		AgentCardURL: server.baseURL() + "/a2a/agent-card.json",
	})
	_, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	})
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected error mentioning `name`, got %v", err)
	}
}

func TestToolRef_LocalMcp_Wire_DeclaresCatalog(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := LocalMcp(LocalMcpSpec{
		Name: "fs",
		URL:  "http://example.invalid/mcp", // unreachable; we seed resolution below
	})
	seedMcpResolution(t, ref,
		map[string]any{"name": "mcp-server-filesystem", "version": "0.4.1"},
		[]map[string]any{
			{
				"name":        "read_file",
				"description": "Read a file.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
				"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
			},
		},
		func(ctx context.Context, name string, args json.RawMessage) (string, error) { return "ok", nil },
	)
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	got := body.Tools[0]
	if got["kind"] != "mcp_local" || got["name"] != "fs" {
		t.Fatalf("unexpected wire shape: %#v", got)
	}
	si, _ := got["serverInfo"].(map[string]any)
	if si == nil || si["name"] != "mcp-server-filesystem" || si["version"] != "0.4.1" {
		t.Fatalf("serverInfo not forwarded: %#v", got["serverInfo"])
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool entry, got %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "fs_read_file" {
		t.Fatalf("tool name wrong (expected auto-prefix): %#v", tool)
	}
	if _, has := tool["inputSchema"]; !has {
		t.Fatalf("inputSchema not forwarded: %#v", tool)
	}
	ann, _ := tool["annotations"].(map[string]any)
	if ann == nil || ann["readOnlyHint"] != true {
		t.Fatalf("annotations not forwarded: %#v", tool["annotations"])
	}
}

func TestLocalMcp_AutoPrefixIsIdempotent(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	ref := LocalMcp(LocalMcpSpec{Name: "fs", URL: "http://example.invalid/mcp"})
	seedMcpResolution(t, ref, nil,
		[]map[string]any{
			{"name": "read_file"},
			// Already prefixed by the upstream — must NOT double-prefix.
			{"name": "fs_list_dir"},
		},
		func(ctx context.Context, name string, args json.RawMessage) (string, error) { return "", nil },
	)
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Tools:        []ToolRef{ref},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	tools, _ := body.Tools[0]["tools"].([]any)
	gotNames := []string{}
	for _, t := range tools {
		gotNames = append(gotNames, t.(map[string]any)["name"].(string))
	}
	if !(len(gotNames) == 2 && gotNames[0] == "fs_read_file" && gotNames[1] == "fs_list_dir") {
		t.Fatalf("expected [fs_read_file, fs_list_dir], got %v", gotNames)
	}
}

func TestLocalMcp_StdioTransportSpec(t *testing.T) {
	// Just exercise the constructor — the resolver isn't run here because
	// we don't call RunAgent.
	ref := LocalMcp(LocalMcpSpec{
		Name:    "fs",
		Command: "mcp-server-filesystem",
		Args:    []string{"."},
		Env:     map[string]string{"FOO": "bar"},
		Cwd:     "/tmp",
	})
	if _, ok := ref.(*localMcpServer); !ok {
		t.Fatalf("expected *localMcpServer, got %T", ref)
	}
}

func TestLocalMcp_RejectsBothAndNeither(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		fn()
	}
	mustPanic("both URL and Command", func() {
		LocalMcp(LocalMcpSpec{Name: "fs", URL: "http://x/mcp", Command: "x"})
	})
	mustPanic("neither URL nor Command", func() {
		LocalMcp(LocalMcpSpec{Name: "fs"})
	})
}

func TestToolRef_PanicsOnInvalidNames(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		fn()
	}
	mustPanic("MantyxA2A bad name", func() {
		MantyxA2A(MantyxA2AOptions{Name: "bad name", AgentCardURL: "https://x"})
	})
	mustPanic("MantyxMcp empty url", func() {
		MantyxMcp(MantyxMcpOptions{Name: "x", URL: ""})
	})
}

// ---------- ReasoningLevel -------------------------------------------------

func TestReasoningLevel_StringForwardedVerbatim(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt:   "x",
		Prompt:         "y",
		ReasoningLevel: ReasoningMedium(),
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastRunCreateBody), `"reasoningLevel":"medium"`) {
		t.Fatalf("expected medium reasoning level in body: %s", server.lastRunCreateBody)
	}
}

func TestReasoningLevel_NumberForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt:   "x",
		Prompt:         "y",
		ReasoningLevel: ReasoningEffort(80),
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastRunCreateBody), `"reasoningLevel":80`) {
		t.Fatalf("expected reasoningLevel=80 in body: %s", server.lastRunCreateBody)
	}
}

func TestReasoningLevel_OmittedWhenNil(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if strings.Contains(string(server.lastRunCreateBody), "reasoningLevel") {
		t.Fatalf("expected reasoningLevel to be omitted: %s", server.lastRunCreateBody)
	}
}

func TestReasoningEffort_PanicsOutOfRange(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for out-of-range value")
		}
	}()
	_ = ReasoningEffort(200)
}

// ---------- OutputSchema --------------------------------------------------

func weatherSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city":          map[string]any{"type": "string"},
			"temperature_c": map[string]any{"type": "number"},
		},
		"required":             []any{"city", "temperature_c"},
		"additionalProperties": false,
	}
}

func TestOutputSchema_ForwardedOnRun(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "{}"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		OutputSchema: &OutputSchema{Name: "weather_report", Schema: weatherSchema()},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastRunCreateBody), `"outputSchema"`) ||
		!strings.Contains(string(server.lastRunCreateBody), `"name":"weather_report"`) ||
		!strings.Contains(string(server.lastRunCreateBody), `"temperature_c"`) {
		t.Fatalf("expected outputSchema in body: %s", server.lastRunCreateBody)
	}
}

func TestOutputSchema_OmittedWhenNil(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if strings.Contains(string(server.lastRunCreateBody), "outputSchema") {
		t.Fatalf("expected outputSchema omitted: %s", server.lastRunCreateBody)
	}
}

func TestOutputSchema_LocalValidationRejectsBadShape(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})

	// Bad name.
	_, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Name: "bad name!", Schema: weatherSchema()},
	})
	if err == nil {
		t.Fatalf("expected error for invalid name")
	}

	// Missing schema.
	_, err = client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Schema: nil},
	})
	if err == nil {
		t.Fatalf("expected error for nil Schema")
	}
}

func TestOutputSchema_RejectedAtSizeLimit(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})

	huge := map[string]any{"type": "object"}
	props := map[string]any{}
	for i := 0; i < 4000; i++ {
		props[fmt.Sprintf("f_%d", i)] = map[string]any{
			"type":        "string",
			"description": "xxxxxxxx",
		}
	}
	huge["properties"] = props

	_, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Schema: huge},
	})
	if err == nil || !strings.Contains(err.Error(), "32 KB") {
		t.Fatalf("expected 32 KB limit error, got: %v", err)
	}
}

func TestOutputSchema_InSessionCreateAndPerMessageOverride(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	session, err := client.CreateSession(context.Background(), SessionSpec{
		SystemPrompt: "x",
		OutputSchema: &OutputSchema{Schema: weatherSchema()},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !strings.Contains(string(server.lastSessionCreateBody), `"outputSchema"`) {
		t.Fatalf("expected outputSchema in session body: %s", server.lastSessionCreateBody)
	}
	override := &OutputSchema{
		Name: "ack",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
			"required":   []any{"ok"},
		},
	}
	if _, err := session.Send(context.Background(), "hi", WithOutputSchema(override)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(string(server.lastSessionMessageBody), `"name":"ack"`) {
		t.Fatalf("expected outputSchema override in send body: %s", server.lastSessionMessageBody)
	}
}

func TestOutputSchema_ReflectedFromGoStruct(t *testing.T) {
	type weatherReport struct {
		City         string  `json:"city" jsonschema:"City name"`
		TemperatureC float64 `json:"temperature_c" jsonschema:"Temperature in Celsius"`
	}

	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{
			kind: "result",
			data: map[string]any{
				"subtype": "success",
				"text":    `{"city":"SF","temperature_c":17.5}`,
			},
		}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})

	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{
			Name:   "weather_report",
			Schema: &weatherReport{},
		},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var body struct {
		OutputSchema struct {
			Name   string         `json:"name"`
			Schema map[string]any `json:"schema"`
		} `json:"outputSchema"`
	}
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body.OutputSchema.Name != "weather_report" {
		t.Fatalf("expected name=weather_report, got %q", body.OutputSchema.Name)
	}
	if body.OutputSchema.Schema["type"] != "object" {
		t.Fatalf("expected schema.type=object, got %#v", body.OutputSchema.Schema["type"])
	}
	props, _ := body.OutputSchema.Schema["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("missing schema.properties: %#v", body.OutputSchema.Schema)
	}
	city, _ := props["city"].(map[string]any)
	if city == nil || city["type"] != "string" || city["description"] != "City name" {
		t.Fatalf("unexpected city schema: %#v", props["city"])
	}
	temp, _ := props["temperature_c"].(map[string]any)
	if temp == nil || temp["type"] != "number" || temp["description"] != "Temperature in Celsius" {
		t.Fatalf("unexpected temperature_c schema: %#v", props["temperature_c"])
	}
	required, _ := body.OutputSchema.Schema["required"].([]any)
	if len(required) != 2 {
		t.Fatalf("expected 2 required entries, got %#v", required)
	}
}

func TestOutputSchema_NameOmittedFromWireWhenEmpty(t *testing.T) {
	type ack struct {
		Ok bool `json:"ok"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "{}"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})

	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Schema: &ack{}},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	out, _ := body["outputSchema"].(map[string]any)
	if out == nil {
		t.Fatalf("expected outputSchema in body: %s", server.lastRunCreateBody)
	}
	if _, has := out["name"]; has {
		t.Fatalf("expected `name` to be omitted when empty, got %#v", out)
	}
	if _, has := out["schema"]; !has {
		t.Fatalf("expected `schema` in outputSchema: %#v", out)
	}
}

func TestOutputSchema_AcceptsRawJSONMessage(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "{}"}}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})

	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Schema: raw},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastRunCreateBody), `"required":["x"]`) {
		t.Fatalf("expected raw schema fragments forwarded: %s", server.lastRunCreateBody)
	}
}

// ---------- ParseRunOutput ------------------------------------------------

func TestParseRunOutput_DecodesIntoStruct(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{
			kind: "result",
			data: map[string]any{
				"subtype": "success",
				"text":    `{"city":"SF","temperature_c":17.5}`,
			},
		}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	result, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
		OutputSchema: &OutputSchema{Schema: weatherSchema()},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var report struct {
		City         string  `json:"city"`
		TemperatureC float64 `json:"temperature_c"`
	}
	if err := ParseRunOutput(result, &report); err != nil {
		t.Fatalf("ParseRunOutput: %v", err)
	}
	if report.City != "SF" || report.TemperatureC != 17.5 {
		t.Fatalf("decoded wrong values: %+v", report)
	}
}

func TestParseRunOutput_ReturnsParseErrorOnNonJSON(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{
			kind: "result",
			data: map[string]any{
				"subtype": "success",
				"text":    "I refuse to answer in JSON.",
			},
		}},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	result, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x", Prompt: "y",
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var dest map[string]any
	err = ParseRunOutput(result, &dest)
	if err == nil {
		t.Fatalf("expected ParseError, got nil")
	}
	var pe *ParseError
	if !errorsAs(err, &pe) {
		t.Fatalf("expected *ParseError, got %T (%v)", err, err)
	}
	if pe.Text != "I refuse to answer in JSON." {
		t.Fatalf("ParseError.Text not preserved: %q", pe.Text)
	}
}

// ---------- Local-tool-call dispatch by `kind` -----------------------------

func TestDispatch_A2ALocal_RoutesByName(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_a2a",
					"name":      "intranet_hr",
					"kind":      "a2a_local",
					// MANTYX echoes the (resolved) Agent Card. The SDK should
					// dispatch by tool `name` and not depend on this field.
					"agentCard": map[string]any{
						"name": "Acme HR",
						"url":  server.baseURL() + "/a2a/rpc",
					},
					"args": map[string]any{"message": "When does PTO reset?"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	server.a2aAgentCard = map[string]any{
		"name": "Acme HR",
		"url":  server.baseURL() + "/a2a/rpc",
	}
	server.a2aReplyText = "On Jan 1."
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalA2A(LocalA2ASpec{
		Name:         "intranet_hr",
		AgentCardURL: server.baseURL() + "/a2a/agent-card.json",
	})
	out, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "ask hr",
		Tools:        []ToolRef{tool},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("unexpected text: %q", out.Text)
	}
	if !strings.Contains(string(server.lastA2ARequest), "When does PTO reset?") {
		t.Fatalf("A2A peer did not receive the message: %s", server.lastA2ARequest)
	}
	if !strings.Contains(string(server.lastToolResultBody), "On Jan 1.") {
		t.Fatalf("server did not receive tool result: %s", server.lastToolResultBody)
	}
}

func TestDispatch_McpLocal_RoutesByServerAndTool(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId":   "tu_mcp",
					"name":        "fs_read_file",
					"kind":        "mcp_local",
					"mcpServer":   "fs",
					"mcpToolName": "fs_read_file",
					"args":        map[string]any{"path": "/etc/hosts"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	gotName := ""
	gotPath := ""
	tool := LocalMcp(LocalMcpSpec{Name: "fs", URL: "http://example.invalid/mcp"})
	seedMcpResolution(t, tool, nil,
		[]map[string]any{
			{"name": "read_file", "description": "Read a file from disk."},
		},
		func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			gotName = name
			var parsed struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &parsed); err != nil {
				return "", err
			}
			gotPath = parsed.Path
			return "127.0.0.1 localhost\n", nil
		},
	)
	out, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "read hosts",
		Tools:        []ToolRef{tool},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("unexpected text: %q", out.Text)
	}
	if gotName != "read_file" {
		t.Fatalf("expected upstream tool name `read_file`, got %q", gotName)
	}
	if gotPath != "/etc/hosts" {
		t.Fatalf("unexpected path forwarded to handler: %q", gotPath)
	}
	if !strings.Contains(string(server.lastToolResultBody), "127.0.0.1 localhost") {
		t.Fatalf("server did not receive tool result: %s", server.lastToolResultBody)
	}
}

func TestDispatch_UnknownKindFallsBackToLocalRegistry(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				// `kind` field is intentionally omitted — older runs don't include it
				data: map[string]any{
					"toolUseId": "tu_legacy",
					"name":      "echo",
					"args":      map[string]any{"value": "hi"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name: "echo",
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			return "result:" + args.Value, nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), "result:hi") {
		t.Fatalf("unexpected tool result: %s", server.lastToolResultBody)
	}
}

func TestDispatch_LocalTool_TypedStructArgs(t *testing.T) {
	type echoArgs struct {
		Value string `json:"value"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_typed",
					"name":      "echo",
					"args":      map[string]any{"value": "abc"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name:       "echo",
		Parameters: &echoArgs{},
		Execute: func(ctx context.Context, args echoArgs) (string, error) {
			return "result:" + args.Value, nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), "result:abc") {
		t.Fatalf("server didn't receive tool result: %s", server.lastToolResultBody)
	}
}

func TestDispatch_LocalTool_TypedPointerArgs(t *testing.T) {
	type echoArgs struct {
		Value string `json:"value"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_ptr",
					"name":      "echo",
					"args":      map[string]any{"value": "ptr"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name:       "echo",
		Parameters: &echoArgs{},
		Execute: func(ctx context.Context, args *echoArgs) (string, error) {
			if args == nil {
				return "nil", nil
			}
			return "result:" + args.Value, nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), "result:ptr") {
		t.Fatalf("server didn't receive tool result: %s", server.lastToolResultBody)
	}
}

func TestDispatch_LocalTool_TypedStructResult(t *testing.T) {
	type resolveArgs struct {
		IDs []int `json:"ids"`
	}
	type resolveResult struct {
		Labels map[int]string `json:"labels"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_typed_result",
					"name":      "resolve_ids",
					"args":      map[string]any{"ids": []any{1, 2}},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name:       "resolve_ids",
		Parameters: &resolveArgs{},
		Execute: func(ctx context.Context, args resolveArgs) (*resolveResult, error) {
			labels := map[int]string{}
			for _, id := range args.IDs {
				labels[id] = fmt.Sprintf("user-%d", id)
			}
			return &resolveResult{Labels: labels}, nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	// The SDK should have JSON-marshaled the *resolveResult on the
	// caller's behalf and shipped it inside the tool-result envelope.
	var posted struct {
		ToolUseID string `json:"toolUseId"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal(server.lastToolResultBody, &posted); err != nil {
		t.Fatalf("parse tool-result body: %v", err)
	}
	if posted.ToolUseID != "tu_typed_result" {
		t.Fatalf("unexpected toolUseId: %q", posted.ToolUseID)
	}
	var decoded resolveResult
	if err := json.Unmarshal([]byte(posted.Result), &decoded); err != nil {
		t.Fatalf("posted result is not valid JSON: %v (raw=%q)", err, posted.Result)
	}
	if got, want := decoded.Labels[1], "user-1"; got != want {
		t.Fatalf("labels[1] = %q, want %q (raw=%q)", got, want, posted.Result)
	}
	if got, want := decoded.Labels[2], "user-2"; got != want {
		t.Fatalf("labels[2] = %q, want %q (raw=%q)", got, want, posted.Result)
	}
}

func TestDispatch_LocalTool_TypedValueStructResult(t *testing.T) {
	type ack struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_value_result",
					"name":      "do_thing",
					"args":      map[string]any{},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name: "do_thing",
		Execute: func(ctx context.Context, _ struct{}) (ack, error) {
			return ack{OK: true, Count: 7}, nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var posted struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(server.lastToolResultBody, &posted); err != nil {
		t.Fatalf("parse tool-result body: %v", err)
	}
	var decoded ack
	if err := json.Unmarshal([]byte(posted.Result), &decoded); err != nil {
		t.Fatalf("posted result is not valid JSON: %v (raw=%q)", err, posted.Result)
	}
	if !decoded.OK || decoded.Count != 7 {
		t.Fatalf("decoded result mismatch: %+v (raw=%q)", decoded, posted.Result)
	}
}

func TestDispatch_LocalTool_TypedRawMessageResult(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_raw_result",
					"name":      "raw_passthrough",
					"args":      map[string]any{},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name: "raw_passthrough",
		Execute: func(ctx context.Context, _ struct{}) (json.RawMessage, error) {
			// Pre-encoded JSON should be forwarded verbatim — neither
			// re-marshaled (which would double-encode it as a string) nor
			// base64'd (the trap json.Marshal sets for plain []byte).
			return json.RawMessage(`{"already":"json"}`), nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var posted struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(server.lastToolResultBody, &posted); err != nil {
		t.Fatalf("parse tool-result body: %v", err)
	}
	if posted.Result != `{"already":"json"}` {
		t.Fatalf("RawMessage not forwarded verbatim: %q", posted.Result)
	}
}

func TestDispatch_LocalTool_TypedResultErrorSkipsEncoding(t *testing.T) {
	type result struct {
		Value string `json:"value"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_typed_err",
					"name":      "boom",
					"args":      map[string]any{},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name: "boom",
		Execute: func(ctx context.Context, _ struct{}) (*result, error) {
			return nil, fmt.Errorf("nope")
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), `"error":"nope"`) {
		t.Fatalf("expected error forwarded as tool error, got %s", server.lastToolResultBody)
	}
	if strings.Contains(string(server.lastToolResultBody), `"result"`) {
		t.Fatalf("expected no result field on error path, got %s", server.lastToolResultBody)
	}
}

func TestLocalTool_PanicsOnInvalidExecuteSignature(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{
			name: "non-function Execute",
			fn: func() {
				LocalTool(LocalToolSpec{Name: "x", Execute: "not a function"})
			},
		},
		{
			name: "wrong arity (1 param)",
			fn: func() {
				LocalTool(LocalToolSpec{Name: "x", Execute: func(ctx context.Context) (string, error) { return "", nil }})
			},
		},
		{
			name: "first param not context",
			fn: func() {
				LocalTool(LocalToolSpec{Name: "x", Execute: func(s string, args struct{}) (string, error) { return "", nil }})
			},
		},
		{
			name: "second return not error",
			fn: func() {
				LocalTool(LocalToolSpec{Name: "x", Execute: func(ctx context.Context, args struct{}) (int, int) { return 0, 0 }})
			},
		},
		{
			name: "nil Execute",
			fn: func() {
				LocalTool(LocalToolSpec{Name: "x"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic for %s", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestDispatch_LocalTool_BadJSONSurfacesAsToolError(t *testing.T) {
	type strictArgs struct {
		N int `json:"n"`
	}
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_bad",
					"name":      "strict",
					"args":      map[string]any{"n": "not-a-number"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	tool := LocalTool(LocalToolSpec{
		Name:       "strict",
		Parameters: &strictArgs{},
		Execute: func(ctx context.Context, args strictArgs) (string, error) {
			return fmt.Sprintf("n=%d", args.N), nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), "decode args for tool") {
		t.Fatalf("expected decode error to surface as tool error: %s", server.lastToolResultBody)
	}
}

func TestDispatch_MissingHandlerSurfacesError(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{
					"toolUseId": "tu_nope",
					"name":      "nope",
					"kind":      "a2a_local",
					"args":      map[string]any{"message": "hi"},
				},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "x",
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !strings.Contains(string(server.lastToolResultBody), "No local A2A handler") {
		t.Fatalf("expected helpful error in tool result: %s", server.lastToolResultBody)
	}
}
