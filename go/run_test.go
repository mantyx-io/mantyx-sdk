package mantyx

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunAgent_OneShotWithDeltas(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{kind: "delta", data: map[string]any{"text": "Hello "}},
			{kind: "delta", data: map[string]any{"text": "world"}},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "Hello world"}},
		},
	}

	client := NewClient(Options{
		APIKey:        "test-key",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	deltas := []string{}
	result, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "hi",
		OnAssistantDelta: func(s string) {
			deltas = append(deltas, s)
		},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if result.Text != "Hello world" {
		t.Fatalf("unexpected text: %q", result.Text)
	}
	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Fatalf("unexpected stream: %q", got)
	}
	if server.lastAuthHeader != "Bearer test-key" {
		t.Fatalf("missing/wrong auth header: %q", server.lastAuthHeader)
	}
}

func TestRunAgent_LocalToolDispatch(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "local_tool_call",
				data: map[string]any{"toolUseId": "t1", "name": "echo", "args": map[string]any{"value": "abc"}},
				wait: true,
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}

	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	tool := LocalTool(LocalToolSpec{
		Name:        "echo",
		Description: "echo",
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
	out, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "go",
		Tools:        []ToolRef{tool},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("unexpected text: %q", out.Text)
	}
	if !strings.Contains(string(server.lastToolResultBody), "result:abc") {
		t.Fatalf("server didn't receive tool result: %s", server.lastToolResultBody)
	}
}

func TestRunAgent_AuthError(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.failAuth = true
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	_, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err == nil {
		t.Fatalf("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
}

func TestRunAgent_AgentIDIsForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}},
		},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	tool := LocalTool(LocalToolSpec{
		Name:        "echo",
		Description: "echo",
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return "", nil
		},
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		AgentID: "agent_abc",
		Prompt:  "hi",
		Tools:   []ToolRef{tool},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body["agentId"] != "agent_abc" {
		t.Fatalf("agentId not forwarded: %#v", body["agentId"])
	}
	if _, exists := body["systemPrompt"]; exists {
		t.Fatalf("systemPrompt should be omitted, got %#v", body["systemPrompt"])
	}
}

func TestRunAgent_RejectsMissingAgentAndPrompt(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{Prompt: "hi"}); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestRunAgent_MetadataIsForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Metadata: map[string]string{
			"customer": "acme",
			"env":      "prod",
		},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	meta, _ := body["metadata"].(map[string]any)
	if meta["customer"] != "acme" || meta["env"] != "prod" {
		t.Fatalf("metadata not forwarded: %#v", body["metadata"])
	}
}

func TestRunAgent_OmitsEmptyMetadata(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		Metadata:     map[string]string{},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if strings.Contains(string(server.lastRunCreateBody), "metadata") {
		t.Fatalf("expected empty Metadata to be omitted, got %s", server.lastRunCreateBody)
	}
}

func TestRunAgent_LoopDetectionIsForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt:   "x",
		Prompt:         "y",
		LoopDetection:  LoopDetectionThresholds(2, 4),
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	ld, _ := body["loopDetection"].(map[string]any)
	if ld["consecutiveThreshold"].(float64) != 2 || ld["hardCutoffThreshold"].(float64) != 4 {
		t.Fatalf("loopDetection not forwarded: %#v", body["loopDetection"])
	}
}

func TestRunAgent_LoopDetectionDisabled(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt:  "x",
		Prompt:        "y",
		LoopDetection: LoopDetectionDisabled(),
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	v, ok := body["loopDetection"]
	if !ok {
		t.Fatalf("loopDetection missing")
	}
	if got, isBool := v.(bool); !isBool || got != false {
		t.Fatalf("expected loopDetection=false sentinel, got %#v", v)
	}
}

func TestRunAgent_LoopDetectionRejectsBadThresholds(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on bad thresholds")
		}
	}()
	_ = LoopDetectionThresholds(5, 5) // hard cutoff must be strictly greater
}

func TestRunAgent_ToolBudgetsIsForwarded(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		ToolBudgets: ToolBudgets{
			"recall":      {MaxCalls: 4},
			"scary_tool":  {MaxCalls: 0},
		},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	tb, _ := body["toolBudgets"].(map[string]any)
	recall, _ := tb["recall"].(map[string]any)
	scary, _ := tb["scary_tool"].(map[string]any)
	if recall["maxCalls"].(float64) != 4 || scary["maxCalls"].(float64) != 0 {
		t.Fatalf("toolBudgets not forwarded: %#v", body["toolBudgets"])
	}
}

func TestRunAgent_ToolBudgetsEmptyMapClearsDefaults(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	if _, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		ToolBudgets:  ToolBudgets{},
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(server.lastRunCreateBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	tb, ok := body["toolBudgets"].(map[string]any)
	if !ok {
		t.Fatalf("toolBudgets missing or wrong type: %#v", body["toolBudgets"])
	}
	if len(tb) != 0 {
		t.Fatalf("expected empty toolBudgets map, got %#v", tb)
	}
}

func TestRunAgent_ToolBudgetsRejectsNegativeMaxCalls(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	_, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		ToolBudgets:  ToolBudgets{"recall": {MaxCalls: -1}},
	})
	if err == nil {
		t.Fatalf("expected validation error for negative MaxCalls")
	}
}

// TestRunAgent_ErrorEventCarriesTriageAttributes covers the truncation
// salvage path from docs/agent-runs-protocol.md §7: the engine emits an
// `assistant_message` with the partial text and a `finishReason`, then a
// terminal `error` event with `errorClass: "truncation"` and the same
// bytes on `partialText`. The SDK should surface those on `*RunError`.
func TestRunAgent_ErrorEventCarriesTriageAttributes(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "assistant_message",
				data: map[string]any{
					"text":         `{"answer":"hello`,
					"turn":         0,
					"finishReason": "max_tokens",
				},
			},
			{
				kind: "error",
				data: map[string]any{
					"error":        "Model output was truncated (stop_reason=max_tokens).",
					"code":         "truncation",
					"errorClass":   "truncation",
					"finishReason": "max_tokens",
					"partialText":  `{"answer":"hello`,
					"retryable":    false,
				},
			},
		},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	_, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err == nil {
		t.Fatalf("expected error")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected *RunError, got %T: %v", err, err)
	}
	if runErr.Code != "truncation" {
		t.Fatalf("expected Code=truncation, got %q", runErr.Code)
	}
	if runErr.ErrorClass != "truncation" {
		t.Fatalf("expected ErrorClass=truncation, got %q", runErr.ErrorClass)
	}
	if runErr.FinishReason != "max_tokens" {
		t.Fatalf("expected FinishReason=max_tokens, got %q", runErr.FinishReason)
	}
	if runErr.PartialText != `{"answer":"hello` {
		t.Fatalf("expected PartialText to carry the salvage bytes, got %q", runErr.PartialText)
	}
	if runErr.Retryable == nil || *runErr.Retryable != false {
		t.Fatalf("expected Retryable=&false, got %v", runErr.Retryable)
	}
	if !strings.Contains(runErr.Message, "truncated") {
		t.Fatalf("expected message to contain 'truncated', got %q", runErr.Message)
	}
}

// TestRunAgent_ErrorEventFallsBackToCode covers older runners that don't
// yet emit `errorClass`; the SDK should still populate `Code` from the
// legacy alias and leave the optional triage fields empty / nil.
func TestRunAgent_ErrorEventFallsBackToCode(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "error",
				data: map[string]any{
					"error": "boom",
					"code":  "worker_error",
				},
			},
		},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	_, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err == nil {
		t.Fatalf("expected error")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected *RunError, got %T: %v", err, err)
	}
	if runErr.Code != "worker_error" {
		t.Fatalf("expected Code=worker_error, got %q", runErr.Code)
	}
	if runErr.ErrorClass != "" {
		t.Fatalf("expected empty ErrorClass, got %q", runErr.ErrorClass)
	}
	if runErr.FinishReason != "" || runErr.PartialText != "" || runErr.Retryable != nil {
		t.Fatalf("expected unset triage attrs, got %+v", runErr)
	}
}

// TestRunAgent_AssistantMessageSurfacesTriageFields verifies that the
// enriched `assistant_message` payload (turn / finishReason / toolCalls)
// is round-tripped through the event stream unchanged.
func TestRunAgent_AssistantMessageSurfacesTriageFields(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "assistant_message",
				data: map[string]any{
					"text":         "calling search",
					"turn":         0,
					"finishReason": "tool_use",
					"toolCalls": []any{
						map[string]any{"id": "call_a", "name": "search", "input": map[string]any{"q": "hi"}},
					},
				},
			},
			{kind: "result", data: map[string]any{"subtype": "success", "text": "done"}},
		},
	}
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	var msg map[string]any
	_, err := client.RunAgent(context.Background(), RunSpec{
		SystemPrompt: "x",
		Prompt:       "y",
		OnEvent: func(ev RunEvent) {
			if ev.Type == "assistant_message" {
				msg = ev.Data
			}
		},
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if msg == nil {
		t.Fatalf("expected an assistant_message event")
	}
	if msg["text"] != "calling search" {
		t.Fatalf("text not forwarded: %#v", msg["text"])
	}
	if msg["finishReason"] != "tool_use" {
		t.Fatalf("finishReason not forwarded: %#v", msg["finishReason"])
	}
	if turn, ok := msg["turn"].(float64); !ok || turn != 0 {
		t.Fatalf("turn not forwarded: %#v", msg["turn"])
	}
	calls, _ := msg["toolCalls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("toolCalls not forwarded: %#v", msg["toolCalls"])
	}
}

// TestRunAgent_SurfacesCostAttributionFromResult covers the
// cost-attribution triple from `docs/agent-runs-protocol.md` §7.1: the
// successful terminal `result` event carries `tokens` / `turns` /
// `model`, which the SDK lifts onto `RunResult`.
func TestRunAgent_SurfacesCostAttributionFromResult(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "result",
				data: map[string]any{
					"subtype": "success",
					"text":    "Hello world",
					"tokens": map[string]any{
						"inputTokens":     1283,
						"cachedTokens":    512,
						"reasoningTokens": 96,
						"outputTokens":    240,
					},
					"turns": 3,
					"model": map[string]any{
						"id":              "platform:demo",
						"provider":        "openai",
						"vendorModelId":   "gpt-test",
						"reasoningEffort": "low",
					},
				},
			},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	out, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Tokens == nil {
		t.Fatalf("expected Tokens to be populated, got nil")
	}
	if out.Tokens.InputTokens != 1283 || out.Tokens.CachedTokens != 512 ||
		out.Tokens.ReasoningTokens != 96 || out.Tokens.OutputTokens != 240 {
		t.Fatalf("unexpected token totals: %+v", out.Tokens)
	}
	if out.Turns != 3 {
		t.Fatalf("expected Turns=3, got %d", out.Turns)
	}
	if out.Model == nil {
		t.Fatalf("expected Model to be populated, got nil")
	}
	if out.Model.ID != "platform:demo" || out.Model.Provider != "openai" ||
		out.Model.VendorModelID != "gpt-test" || out.Model.ReasoningEffort != "low" {
		t.Fatalf("unexpected model: %+v", out.Model)
	}
}

// TestRunAgent_LegacyServerOmitsCostAttribution verifies that
// terminal events without the cost-attribution triple leave the
// fields at their zero values — that's the "no usage data" sentinel
// callers detect via `Result.Model == nil`.
func TestRunAgent_LegacyServerOmitsCostAttribution(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	out, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Tokens != nil {
		t.Fatalf("expected Tokens=nil against legacy server, got %+v", out.Tokens)
	}
	if out.Turns != 0 {
		t.Fatalf("expected Turns=0, got %d", out.Turns)
	}
	if out.Model != nil {
		t.Fatalf("expected Model=nil against legacy server, got %+v", out.Model)
	}
}

// TestRunAgent_ErrorEventCarriesCostAttribution mirrors the success
// path for the truncation salvage: a terminal `error` event now also
// carries `tokens` / `turns` / `model` so callers can attribute spend
// for failed runs too. See `docs/agent-runs-protocol.md` §7.1.
func TestRunAgent_ErrorEventCarriesCostAttribution(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "error",
				data: map[string]any{
					"error":        "Model output was truncated (stop_reason=max_tokens).",
					"errorClass":   "truncation",
					"finishReason": "max_tokens",
					"partialText":  `{"answer":"hello`,
					"retryable":    false,
					"tokens": map[string]any{
						"inputTokens":     8190,
						"cachedTokens":    0,
						"reasoningTokens": 0,
						"outputTokens":    1024,
					},
					"turns": 1,
					"model": map[string]any{
						"id":            "provider:cmf",
						"provider":      "google",
						"vendorModelId": "gemini-2.5-pro",
					},
				},
			},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	_, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err == nil {
		t.Fatalf("expected error")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected *RunError, got %T: %v", err, err)
	}
	if runErr.Tokens == nil {
		t.Fatalf("expected RunError.Tokens to be populated, got nil")
	}
	if runErr.Tokens.InputTokens != 8190 || runErr.Tokens.OutputTokens != 1024 {
		t.Fatalf("unexpected error token totals: %+v", runErr.Tokens)
	}
	if runErr.Turns != 1 {
		t.Fatalf("expected RunError.Turns=1, got %d", runErr.Turns)
	}
	if runErr.Model == nil || runErr.Model.Provider != "google" {
		t.Fatalf("expected RunError.Model.Provider=google, got %+v", runErr.Model)
	}
}

// TestRunAgent_ClampsMalformedTokenBuckets verifies the SDK clamps
// negatives / non-numbers to zero so dashboards never see garbage.
func TestRunAgent_ClampsMalformedTokenBuckets(t *testing.T) {
	server := newMockServer()
	defer server.close()
	server.scriptForNextRun = &runScript{
		events: []scriptEvent{
			{
				kind: "result",
				data: map[string]any{
					"subtype": "success",
					"text":    "ok",
					"tokens": map[string]any{
						"inputTokens":     -10,
						"cachedTokens":    "not a number",
						"outputTokens":    12.7,
					},
					"turns": -1,
					"model": map[string]any{"id": "x", "provider": "openai", "vendorModelId": "y"},
				},
			},
		},
	}
	client := NewClient(Options{APIKey: "k", WorkspaceSlug: "demo", BaseURL: server.baseURL()})
	out, err := client.RunAgent(context.Background(), RunSpec{SystemPrompt: "x", Prompt: "y"})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.Tokens == nil {
		t.Fatalf("expected Tokens to be populated")
	}
	if out.Tokens.InputTokens != 0 || out.Tokens.CachedTokens != 0 ||
		out.Tokens.ReasoningTokens != 0 || out.Tokens.OutputTokens != 12 {
		t.Fatalf("expected clamped token totals, got %+v", out.Tokens)
	}
	if out.Turns != 0 {
		t.Fatalf("expected clamped Turns=0, got %d", out.Turns)
	}
}

func TestListModels(t *testing.T) {
	server := newMockServer()
	defer server.close()
	client := NewClient(Options{
		APIKey:        "k",
		WorkspaceSlug: "demo",
		BaseURL:       server.baseURL(),
	})
	out, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "platform:demo" {
		t.Fatalf("unexpected catalog: %+v", out)
	}
	if out.DefaultModelID != "platform:demo" {
		t.Fatalf("expected default %q, got %q", "platform:demo", out.DefaultModelID)
	}
}
