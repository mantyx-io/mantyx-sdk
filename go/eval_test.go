package mantyx

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestListEvalDatasets(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	out, err := client.ListEvalDatasets(context.Background())
	if err != nil {
		t.Fatalf("ListEvalDatasets: %v", err)
	}
	if len(out.Datasets) != 1 || out.Datasets[0].ID != "ds_demo" {
		t.Fatalf("unexpected datasets: %#v", out.Datasets)
	}
}

func TestGetEvalDataset(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	out, err := client.GetEvalDataset(context.Background(), "ds_demo")
	if err != nil {
		t.Fatalf("GetEvalDataset: %v", err)
	}
	if len(out.Cases) != 1 || out.Cases[0].Name != "hello" {
		t.Fatalf("unexpected cases: %#v", out.Cases)
	}
}

func TestCreateAndGetEvalRun(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	accepted, err := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{
		DatasetID: "ds_demo",
		AgentID:   "agent_1",
	})
	if err != nil {
		t.Fatalf("CreateEvalRun: %v", err)
	}
	if accepted.RunID == "" {
		t.Fatal("expected run id")
	}
	if srv.lastEvalCreateBody == nil {
		t.Fatal("expected create body to be recorded")
	}

	detail, err := client.GetEvalRun(context.Background(), accepted.RunID)
	if err != nil {
		t.Fatalf("GetEvalRun: %v", err)
	}
	if detail.Status != EvalRunSucceeded {
		t.Fatalf("status: %q", detail.Status)
	}
	if detail.PassedCases != 1 {
		t.Fatalf("passedCases: %d", detail.PassedCases)
	}
}

func TestCreateEvalRunValidation(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	_, err := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{AgentID: "a1"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestStreamEvalRun(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	accepted, err := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{
		DatasetID: "ds_demo",
		AgentID:   "agent_1",
	})
	if err != nil {
		t.Fatalf("CreateEvalRun: %v", err)
	}

	events, errs := client.StreamEvalRun(context.Background(), accepted.RunID)
	var types []string
	for ev := range events {
		types = append(types, ev.Type)
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(types) < 2 || types[len(types)-1] != "run_completed" {
		t.Fatalf("event types: %v", types)
	}
}

func TestRunEval(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	detail, err := client.RunEval(context.Background(), CreateEvalRunRequest{
		DatasetID: "ds_demo",
		AgentID:   "agent_1",
	}, RunEvalOptions{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if detail.Status != EvalRunSucceeded {
		t.Fatalf("status: %q", detail.Status)
	}
}

func TestCompareEvalRuns(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	a, _ := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{DatasetID: "ds_demo", AgentID: "agent_1"})
	b, _ := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{DatasetID: "ds_demo", AgentID: "agent_1", ModelID: "m1"})
	cmp, err := client.CompareEvalRuns(context.Background(), a.RunID, b.RunID)
	if err != nil {
		t.Fatalf("CompareEvalRuns: %v", err)
	}
	if cmp.RunA.ID != a.RunID || cmp.RunB.ID != b.RunID {
		t.Fatalf("compare ids: %#v %#v", cmp.RunA.ID, cmp.RunB.ID)
	}
}

func TestCancelEvalRun(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	accepted, _ := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{DatasetID: "ds_demo", AgentID: "agent_1"})
	if err := client.CancelEvalRun(context.Background(), accepted.RunID); err != nil {
		t.Fatalf("CancelEvalRun: %v", err)
	}
	detail, _ := client.GetEvalRun(context.Background(), accepted.RunID)
	if detail.Status != EvalRunCancelled {
		t.Fatalf("status: %q", detail.Status)
	}
}

func TestCreateEvalRunInlineLocalTool(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	tool := LocalTool(LocalToolSpec{
		Name: "echo",
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	_, err := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{
		Dataset: &InlineEvalDatasetSpec{
			Cases: []InlineEvalCaseSpec{{Input: map[string]any{"role": "user", "content": "ping"}}},
		},
		Agent: &RunSpec{SystemPrompt: "You are helpful.", Tools: []ToolRef{tool}},
	})
	if err != nil {
		t.Fatalf("CreateEvalRun: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(srv.lastEvalCreateBody, &body); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	agent, _ := body["agent"].(map[string]any)
	tools, _ := agent["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %#v", tools)
	}
	wire, _ := tools[0].(map[string]any)
	if wire["kind"] != "local" || wire["name"] != "echo" {
		t.Fatalf("unexpected tool wire: %#v", wire)
	}
}

func TestCreateEvalRunSavedAgentLocalTools(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	tool := LocalTool(LocalToolSpec{
		Name: "echo",
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	_, err := client.CreateEvalRun(context.Background(), CreateEvalRunRequest{
		DatasetID: "ds_demo",
		AgentID:   "agent_1",
		Tools:     []ToolRef{tool},
	})
	if err != nil {
		t.Fatalf("CreateEvalRun: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(srv.lastEvalCreateBody, &body); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	tools, _ := body["tools"].([]any)
	wire, _ := tools[0].(map[string]any)
	if wire["kind"] != "local" || wire["name"] != "echo" {
		t.Fatalf("unexpected tool wire: %#v", wire)
	}
}

func TestRunEvalDispatchesLocalTool(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	client := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "acme", BaseURL: srv.baseURL()})

	tool := LocalTool(LocalToolSpec{
		Name: "echo",
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Msg string `json:"msg"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			return args.Msg, nil
		},
	})
	agentRunID := newID("run")
	srv.startRun(agentRunID, &runScript{
		events: []scriptEvent{{
			kind: "local_tool_call",
			data: map[string]any{"toolUseId": "tu_eval", "name": "echo", "args": map[string]any{"msg": "hi"}},
			wait: true,
		}, {
			kind: "result",
			data: map[string]any{"subtype": "success", "text": "ok"},
		}},
	})
	srv.evalStreamEvents = []map[string]any{{
		"type": "local_tool_call", "agentRunId": agentRunID,
		"toolUseId": "tu_eval", "name": "echo", "args": map[string]any{"msg": "hi"},
	}, {"type": "run_completed"}}

	detail, err := client.RunEval(context.Background(), CreateEvalRunRequest{
		DatasetID: "ds_demo",
		AgentID:   "agent_1",
	}, RunEvalOptions{Tools: []ToolRef{tool}, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if detail.Status != EvalRunSucceeded {
		t.Fatalf("status: %q", detail.Status)
	}
	var posted struct {
		ToolUseID string `json:"toolUseId"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal(srv.lastToolResultBody, &posted); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if posted.ToolUseID != "tu_eval" || posted.Result != "hi" {
		t.Fatalf("tool result: %#v", posted)
	}
}
