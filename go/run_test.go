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
