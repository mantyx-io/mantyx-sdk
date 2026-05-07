package a2asrv_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2asrvpkg "github.com/a2aproject/a2a-go/v2/a2asrv"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
	srv "github.com/mantyx-io/mantyx-sdk/go/a2asrv"
)

// minimal stub that captures the HTTP requests the MANTYX client makes and
// scripts a deterministic SSE stream, sufficient for exercising the
// MantyxAgentExecutor end-to-end without standing up the real backend.
type stubBackend struct {
	t  *testing.T
	mu sync.Mutex

	lastRunBody         map[string]any
	lastSessionBody     map[string]any
	lastSessionMsgBody  map[string]any
	createSessionCount  int
	streamScript        []string // assistant_delta texts to emit
	finalText           string
	finalSubtype        string // "" → "success"

	srv *httptest.Server
}

func newStubBackend(t *testing.T) *stubBackend {
	t.Helper()
	b := &stubBackend{t: t, finalSubtype: "success"}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *stubBackend) handle(w http.ResponseWriter, r *http.Request) {
	// Strip the SDK's `/api/v1/workspaces/<slug>` prefix so the routing logic
	// below stays focused on the resource paths.
	path := r.URL.Path
	if i := strings.Index(path, "/agent-"); i >= 0 {
		path = path[i:]
	}
	switch {
	case r.Method == http.MethodPost && path == "/agent-runs":
		body := readJSON(b.t, r)
		b.mu.Lock()
		b.lastRunBody = body
		b.mu.Unlock()
		runID := "run_oneshot"
		writeJSON(w, map[string]any{
			"runId":     runID,
			"streamUrl": "/agent-runs/" + runID + "/stream",
		})

	case r.Method == http.MethodPost && path == "/agent-sessions":
		body := readJSON(b.t, r)
		b.mu.Lock()
		b.lastSessionBody = body
		b.createSessionCount++
		count := b.createSessionCount
		b.mu.Unlock()
		writeJSON(w, map[string]any{"id": "sess_" + itoa(count)})

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/agent-sessions/") &&
		strings.HasSuffix(path, "/messages"):
		body := readJSON(b.t, r)
		b.mu.Lock()
		b.lastSessionMsgBody = body
		b.mu.Unlock()
		writeJSON(w, map[string]any{
			"runId":     "run_session",
			"streamUrl": path,
		})

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/stream"):
		b.writeSSE(w)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/cancel"):
		writeJSON(w, map[string]any{"ok": true})

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/agent-sessions/"):
		writeJSON(w, map[string]any{"ok": true})

	default:
		http.NotFound(w, r)
	}
}

func (b *stubBackend) writeSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	seq := 1
	for _, delta := range b.streamScript {
		writeEvent(w, seq, "assistant_delta", `{"text":`+jsonString(delta)+`}`)
		seq++
		flusher.Flush()
	}
	subtype := b.finalSubtype
	if subtype == "" {
		subtype = "success"
	}
	writeEvent(w, seq, "result",
		`{"subtype":`+jsonString(subtype)+`,"text":`+jsonString(b.finalText)+`}`)
	flusher.Flush()
}

func TestExecutorAgentSpecValidation(t *testing.T) {
	client := mantyx.NewClient(mantyx.Options{APIKey: "k", WorkspaceSlug: "ws", BaseURL: "http://x"})
	if _, err := srv.NewExecutor(client, srv.AgentSpec{}, srv.ExecutorOptions{}); err == nil {
		t.Fatalf("expected error for empty AgentSpec")
	}
	if _, err := srv.NewExecutor(client, srv.AgentSpec{AgentID: "agent_x"}, srv.ExecutorOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := srv.NewExecutor(client, srv.AgentSpec{SystemPrompt: "you are helpful"}, srv.ExecutorOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecutorStatelessEmitsSubmittedWorkingCompleted(t *testing.T) {
	backend := newStubBackend(t)
	backend.streamScript = []string{"Hi ", "there"}
	backend.finalText = "Hi there"

	client := mantyx.NewClient(mantyx.Options{
		APIKey:        "k",
		WorkspaceSlug: "ws",
		BaseURL:       backend.srv.URL,
	})
	exec, err := srv.NewExecutor(client, srv.AgentSpec{SystemPrompt: "you are helpful"}, srv.ExecutorOptions{
		Conversation: srv.ConversationStateless,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	execCtx := &a2asrvpkg.ExecutorContext{
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
		TaskID:    a2a.TaskID("task_1"),
		ContextID: "ctx_1",
	}

	events := collectEvents(t, exec.Execute(context.Background(), execCtx))

	// Initial Task event then a working status update.
	if _, ok := events[0].(*a2a.Task); !ok {
		t.Fatalf("expected initial *a2a.Task, got %T", events[0])
	}
	first := events[1].(*a2a.TaskStatusUpdateEvent)
	if first.Status.State != a2a.TaskStateWorking {
		t.Fatalf("expected working status update, got %v", first.Status.State)
	}

	// Two delta events, each carrying a working status with a Message body.
	deltaEvents := filterStatusUpdatesWithMessage(events)
	if len(deltaEvents) < 2 {
		t.Fatalf("expected ≥ 2 delta status updates, got %d (events=%+v)", len(deltaEvents), events)
	}
	if got := messageText(deltaEvents[0].Status.Message); got != "Hi " {
		t.Fatalf("first delta = %q, want %q", got, "Hi ")
	}
	if got := messageText(deltaEvents[1].Status.Message); got != "there" {
		t.Fatalf("second delta = %q, want %q", got, "there")
	}

	last := events[len(events)-1].(*a2a.TaskStatusUpdateEvent)
	if last.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("expected final completed, got %v", last.Status.State)
	}
	if got := messageText(last.Status.Message); got != "Hi there" {
		t.Fatalf("final text = %q, want %q", got, "Hi there")
	}

	// Stateless mode hits /agent-runs only.
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lastRunBody == nil || backend.lastRunBody["systemPrompt"] != "you are helpful" {
		t.Fatalf("expected /agent-runs to receive systemPrompt, got %+v", backend.lastRunBody)
	}
	if backend.lastSessionBody != nil {
		t.Fatalf("did not expect session create, got %+v", backend.lastSessionBody)
	}
}

func TestExecutorAutoReusesSessionPerContext(t *testing.T) {
	backend := newStubBackend(t)
	backend.streamScript = []string{}
	backend.finalText = "first"

	client := mantyx.NewClient(mantyx.Options{
		APIKey:        "k",
		WorkspaceSlug: "ws",
		BaseURL:       backend.srv.URL,
	})
	exec, err := srv.NewExecutor(client, srv.AgentSpec{AgentID: "agent_xyz"}, srv.ExecutorOptions{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	execCtx := &a2asrvpkg.ExecutorContext{
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		TaskID:    a2a.TaskID("task_1"),
		ContextID: "ctx_one",
	}
	collectEvents(t, exec.Execute(context.Background(), execCtx))

	backend.mu.Lock()
	if backend.createSessionCount != 1 {
		t.Fatalf("expected 1 session create, got %d", backend.createSessionCount)
	}
	if backend.lastSessionBody["agentId"] != "agent_xyz" {
		t.Fatalf("expected agentId=agent_xyz, got %+v", backend.lastSessionBody)
	}
	meta := backend.lastSessionBody["metadata"].(map[string]any)
	if meta["a2a_context_id"] != "ctx_one" {
		t.Fatalf("expected a2a_context_id metadata, got %+v", meta)
	}
	backend.mu.Unlock()

	// Second turn with the same contextID reuses the session.
	execCtx2 := &a2asrvpkg.ExecutorContext{
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("follow-up")),
		TaskID:    a2a.TaskID("task_2"),
		ContextID: "ctx_one",
	}
	collectEvents(t, exec.Execute(context.Background(), execCtx2))

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.createSessionCount != 1 {
		t.Fatalf("expected session reuse, got %d session creates", backend.createSessionCount)
	}
	if backend.lastSessionMsgBody["prompt"] != "follow-up" {
		t.Fatalf("expected /messages prompt=follow-up, got %+v", backend.lastSessionMsgBody)
	}
}

func TestExecutorPublishesFailedOnRunError(t *testing.T) {
	backend := newStubBackend(t)
	backend.streamScript = []string{}
	backend.finalText = "boom"
	backend.finalSubtype = "error_internal"

	client := mantyx.NewClient(mantyx.Options{
		APIKey:        "k",
		WorkspaceSlug: "ws",
		BaseURL:       backend.srv.URL,
	})
	exec, err := srv.NewExecutor(client, srv.AgentSpec{SystemPrompt: "you are helpful"}, srv.ExecutorOptions{
		Conversation: srv.ConversationStateless,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	execCtx := &a2asrvpkg.ExecutorContext{
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		TaskID:    a2a.TaskID("task_err"),
		ContextID: "ctx_err",
	}
	events := collectEvents(t, exec.Execute(context.Background(), execCtx))
	last := events[len(events)-1].(*a2a.TaskStatusUpdateEvent)
	if last.Status.State != a2a.TaskStateFailed {
		t.Fatalf("expected failed, got %v", last.Status.State)
	}
}

func TestServeServesAgentCardAndJSONRPC(t *testing.T) {
	backend := newStubBackend(t)
	backend.streamScript = []string{}
	backend.finalText = "Hello, A2A!"

	client := mantyx.NewClient(mantyx.Options{
		APIKey:        "k",
		WorkspaceSlug: "ws",
		BaseURL:       backend.srv.URL,
	})

	card := srv.NewSimpleAgentCard(
		"MANTYX Test", "test agent", "1.0.0", "http://localhost:0/",
	)

	handle, err := srv.Serve(context.Background(), srv.ServeOptions{
		Client:    client,
		Agent:     srv.AgentSpec{SystemPrompt: "you are helpful"},
		AgentCard: card,
		Addr:      "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close(context.Background()) })

	// Probe the well-known card.
	httpClient := &http.Client{Timeout: 3 * time.Second}
	resp, err := httpClient.Get(handle.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET agent card: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("agent card status = %d", resp.StatusCode)
	}
	body, _ := readBody(resp)
	if !strings.Contains(body, `"MANTYX Test"`) {
		t.Fatalf("agent card body did not contain MANTYX Test: %s", body)
	}

	// JSON-RPC SendMessage round-trip (a2a-go v1 protocol).
	rpc := strings.NewReader(`{
        "jsonrpc": "2.0",
        "id": 1,
        "method": "SendMessage",
        "params": {
            "message": {
                "messageId": "u1",
                "role": "ROLE_USER",
                "parts": [{"text": "Hi!"}]
            }
        }
    }`)
	resp2, err := httpClient.Post(handle.URL+"/", "application/json", rpc)
	if err != nil {
		t.Fatalf("RPC: %v", err)
	}
	body2, _ := readBody(resp2)
	if !strings.Contains(body2, "Hello, A2A!") {
		t.Fatalf("RPC response did not contain final text: %s", body2)
	}
}

// ----------------------------------------------------------- helpers

func collectEvents(t *testing.T, seq func(yield func(a2a.Event, error) bool)) []a2a.Event {
	t.Helper()
	out := []a2a.Event{}
	for ev, err := range seq {
		if err != nil {
			t.Fatalf("executor returned err: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func filterStatusUpdatesWithMessage(events []a2a.Event) []*a2a.TaskStatusUpdateEvent {
	var out []*a2a.TaskStatusUpdateEvent
	for _, e := range events {
		if su, ok := e.(*a2a.TaskStatusUpdateEvent); ok && su.Status.State == a2a.TaskStateWorking && su.Status.Message != nil {
			out = append(out, su)
		}
	}
	return out
}

func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range msg.Parts {
		if t, ok := p.Content.(a2a.Text); ok {
			b.WriteString(string(t))
		}
	}
	return b.String()
}

func readBody(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

func itoa(n int) string { return strings.TrimLeft(strings.NewReplacer().Replace(""+itoaCore(n)), "") }

func itoaCore(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if neg {
		out = append([]byte{'-'}, out...)
	}
	return string(out)
}

func jsonString(s string) string {
	// Tiny JSON-string encoder for the SSE script — we know we never feed
	// control characters, so it's enough to escape backslashes and quotes.
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func writeEvent(w http.ResponseWriter, seq int, kind, dataJSON string) {
	body := "id: " + itoa(seq) + "\n" +
		"event: " + kind + "\n" +
		"data: " + dataJSON + "\n\n"
	_, _ = w.Write([]byte(body))
}

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	defer r.Body.Close()
	var out map[string]any
	if err := decodeJSON(r.Body, &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = encodeJSON(w, v)
}
