package mantyx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// mockServer mirrors the subset of the MANTYX agent-runs HTTP surface used by
// the SDK tests. Each test instantiates a fresh `*mockServer` (one per test
// to avoid state bleed between table-driven cases).
type mockServer struct {
	srv *httptest.Server

	mu                     sync.Mutex
	scriptForNextRun       *runScript
	failAuth               bool
	lastAuthHeader         string
	lastToolResultBody     []byte
	lastRunCreateBody      []byte
	lastSessionCreateBody  []byte
	lastSessionMessageBody []byte
	models                 ModelCatalog
	runs                   map[string]*runState
	sessions               map[string][]Message
	sessionScripts         map[string]*runScript

	// A2A test peer (served at /a2a/...).
	a2aAgentCard    map[string]any // GET /a2a/agent-card.json
	a2aReplyText    string         // text portion of POST /a2a/rpc reply
	lastA2ARequest  []byte
	a2aAuthHeader   string
}

type runScript struct {
	events    []scriptEvent
	finalText string
}

type scriptEvent struct {
	kind   string                 // "delta" | "result" | "local_tool_call"
	data   map[string]any
	wait   bool                   // for local_tool_call: pause until result posted
}

type runState struct {
	id        string
	mu        sync.Mutex
	events    []map[string]any
	pending   chan scriptEvent
	notifiers map[chan struct{}]struct{}
	done      bool
	resolves  map[string]chan struct{}
}

func newMockServer() *mockServer {
	m := &mockServer{
		runs:           map[string]*runState{},
		sessions:       map[string][]Message{},
		sessionScripts: map[string]*runScript{},
		models: ModelCatalog{
			Models: []ModelInfo{{
				ID:                  "platform:demo",
				Label:               "Demo Platform",
				Provider:            "openai",
				VendorModelID:       "gpt-test",
				Source:              "platform_offering",
				ContextWindowTokens: 8000,
			}},
			DefaultModelID: "platform:demo",
		},
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockServer) close() { m.srv.Close() }

func (m *mockServer) baseURL() string { return m.srv.URL }

func (m *mockServer) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/a2a/") {
		m.handleA2A(w, r)
		return
	}
	m.mu.Lock()
	m.lastAuthHeader = r.Header.Get("Authorization")
	failAuth := m.failAuth
	m.mu.Unlock()
	if failAuth {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"Invalid API key"}`)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "workspaces" {
		http.NotFound(w, r)
		return
	}
	rest := parts[4:]
	switch {
	case len(rest) == 1 && rest[0] == "models" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, m.models)
	case len(rest) >= 1 && rest[0] == "agent-runs":
		m.handleAgentRuns(w, r, rest[1:])
	case len(rest) >= 1 && rest[0] == "agent-sessions":
		m.handleAgentSessions(w, r, rest[1:])
	default:
		http.NotFound(w, r)
	}
}

func (m *mockServer) handleAgentRuns(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastRunCreateBody = raw
		script := m.scriptForNextRun
		m.scriptForNextRun = nil
		m.mu.Unlock()
		if script == nil {
			script = &runScript{events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "ok"}}}}
		}
		runID := newID("run")
		m.startRun(runID, script)
		m.writeJSON(w, http.StatusAccepted, map[string]string{
			"runId":     runID,
			"streamUrl": fmt.Sprintf("/api/v1/workspaces/x/agent-runs/%s/stream", runID),
		})
	case len(rest) == 2 && rest[1] == "stream" && r.Method == http.MethodGet:
		m.handleSseStream(w, r, rest[0])
	case len(rest) == 2 && rest[1] == "tool-results" && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastToolResultBody = raw
		m.mu.Unlock()
		var body struct {
			ToolUseID string `json:"toolUseId"`
			Result    string `json:"result"`
			Error     string `json:"error"`
		}
		_ = json.Unmarshal(raw, &body)
		state := m.runs[rest[0]]
		if state != nil {
			state.mu.Lock()
			ch, ok := state.resolves[body.ToolUseID]
			delete(state.resolves, body.ToolUseID)
			state.mu.Unlock()
			if ok {
				close(ch)
			}
		}
		m.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case len(rest) == 2 && rest[1] == "cancel" && r.Method == http.MethodPost:
		state := m.runs[rest[0]]
		if state != nil {
			state.mu.Lock()
			state.done = true
			for n := range state.notifiers {
				close(n)
			}
			state.notifiers = map[chan struct{}]struct{}{}
			state.mu.Unlock()
		}
		m.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "cancelled"})
	default:
		http.NotFound(w, r)
	}
}

func (m *mockServer) handleA2A(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/a2a/agent-card.json" && r.Method == http.MethodGet:
		m.mu.Lock()
		card := m.a2aAgentCard
		m.a2aAuthHeader = r.Header.Get("Authorization")
		m.mu.Unlock()
		if card == nil {
			card = map[string]any{
				"name": "mock-a2a-peer",
				"url":  m.srv.URL + "/a2a/rpc",
			}
		}
		m.writeJSON(w, http.StatusOK, card)
	case r.URL.Path == "/a2a/rpc" && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastA2ARequest = raw
		m.a2aAuthHeader = r.Header.Get("Authorization")
		text := m.a2aReplyText
		m.mu.Unlock()
		if text == "" {
			text = "ok"
		}
		var rpc map[string]any
		_ = json.Unmarshal(raw, &rpc)
		m.writeJSON(w, http.StatusOK, map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc["id"],
			"result": map[string]any{
				"messageId": "reply",
				"role":      "agent",
				"parts": []any{
					map[string]any{"kind": "text", "text": text},
				},
			},
		})
	default:
		http.NotFound(w, r)
	}
}

func (m *mockServer) handleAgentSessions(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastSessionCreateBody = raw
		m.mu.Unlock()
		id := newID("sess")
		m.mu.Lock()
		m.sessions[id] = []Message{}
		m.mu.Unlock()
		m.writeJSON(w, http.StatusCreated, map[string]any{
			"sessionId": id,
			"name":      "ephemeral",
			"createdAt": "now",
		})
	case len(rest) == 1 && r.Method == http.MethodGet:
		m.mu.Lock()
		msgs, ok := m.sessions[rest[0]]
		m.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"Session not found"}`, http.StatusNotFound)
			return
		}
		m.writeJSON(w, http.StatusOK, SessionInfo{
			ID:       rest[0],
			Name:     "ephemeral",
			Status:   "active",
			Messages: msgs,
		})
	case len(rest) == 1 && r.Method == http.MethodDelete:
		m.mu.Lock()
		delete(m.sessions, rest[0])
		m.mu.Unlock()
		m.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case len(rest) == 2 && rest[1] == "messages" && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastSessionMessageBody = raw
		m.mu.Unlock()
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(raw, &body)
		sessionID := rest[0]
		m.mu.Lock()
		_, ok := m.sessions[sessionID]
		script := m.sessionScripts[sessionID]
		delete(m.sessionScripts, sessionID)
		m.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"Session not found"}`, http.StatusNotFound)
			return
		}
		if script == nil {
			script = &runScript{events: []scriptEvent{{kind: "result", data: map[string]any{"subtype": "success", "text": "echo:" + body.Prompt}}}}
		}
		finalText := lastResultText(script)
		m.mu.Lock()
		m.sessions[sessionID] = append(m.sessions[sessionID],
			Message{Role: "user", Content: body.Prompt},
			Message{Role: "assistant", Content: finalText},
		)
		m.mu.Unlock()
		runID := newID("run")
		m.startRun(runID, script)
		m.writeJSON(w, http.StatusAccepted, map[string]string{
			"runId":     runID,
			"streamUrl": fmt.Sprintf("/api/v1/workspaces/x/agent-runs/%s/stream", runID),
		})
	default:
		http.NotFound(w, r)
	}
}

func (m *mockServer) startRun(id string, script *runScript) {
	state := &runState{
		id:        id,
		notifiers: map[chan struct{}]struct{}{},
		resolves:  map[string]chan struct{}{},
	}
	m.mu.Lock()
	m.runs[id] = state
	m.mu.Unlock()

	go func() {
		for _, ev := range script.events {
			state.mu.Lock()
			seq := len(state.events) + 1
			data := map[string]any{"seq": seq}
			for k, v := range ev.data {
				data[k] = v
			}
			payload := map[string]any{"type": eventTypeFor(ev), "data": data}
			state.events = append(state.events, payload)
			for n := range state.notifiers {
				select {
				case n <- struct{}{}:
				default:
				}
			}
			waitCh := state.resolves
			state.mu.Unlock()

			if ev.kind == "local_tool_call" && ev.wait {
				toolUseID, _ := ev.data["toolUseId"].(string)
				ch := make(chan struct{})
				state.mu.Lock()
				state.resolves[toolUseID] = ch
				state.mu.Unlock()
				<-ch
			}
			_ = waitCh

			if ev.kind == "result" || ev.kind == "error" || ev.kind == "cancelled" {
				state.mu.Lock()
				state.done = true
				state.mu.Unlock()
				return
			}
		}
		state.mu.Lock()
		if !state.done {
			seq := len(state.events) + 1
			state.events = append(state.events, map[string]any{
				"type": "result",
				"data": map[string]any{"seq": seq, "subtype": "success", "text": script.finalText},
			})
			state.done = true
			for n := range state.notifiers {
				select {
				case n <- struct{}{}:
				default:
				}
			}
		}
		state.mu.Unlock()
	}()
}

func eventTypeFor(ev scriptEvent) string {
	switch ev.kind {
	case "delta":
		return "assistant_delta"
	case "result":
		return "result"
	case "local_tool_call":
		return "local_tool_call"
	}
	return ev.kind
}

func (m *mockServer) handleSseStream(w http.ResponseWriter, r *http.Request, runID string) {
	m.mu.Lock()
	state, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	fromSeq := 0
	if v := r.URL.Query().Get("lastSeq"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			fromSeq = n
		}
	}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			fromSeq = n
		}
	}

	notify := make(chan struct{}, 8)
	state.mu.Lock()
	state.notifiers[notify] = struct{}{}
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		delete(state.notifiers, notify)
		state.mu.Unlock()
	}()

	cursor := fromSeq
	flush := func() bool {
		state.mu.Lock()
		copyEvents := append([]map[string]any{}, state.events...)
		done := state.done
		state.mu.Unlock()
		for _, ev := range copyEvents {
			data, _ := ev["data"].(map[string]any)
			seq := 0
			if v, ok := data["seq"].(int); ok {
				seq = v
			}
			if seq <= cursor {
				continue
			}
			fmt.Fprintf(w, "id: %d\n", seq)
			fmt.Fprintf(w, "event: %s\n", ev["type"])
			raw, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			if flusher != nil {
				flusher.Flush()
			}
			cursor = seq
		}
		return done
	}

	if flush() && state.events[len(state.events)-1]["type"] == "result" {
		return
	}

	for {
		select {
		case <-notify:
			if flush() {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (m *mockServer) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	raw, _ := json.Marshal(body)
	_, _ = w.Write(raw)
}

func newID(prefix string) string {
	idCounter++
	return fmt.Sprintf("%s_%d", prefix, idCounter)
}

func lastResultText(script *runScript) string {
	for i := len(script.events) - 1; i >= 0; i-- {
		if script.events[i].kind == "result" {
			if t, ok := script.events[i].data["text"].(string); ok {
				return t
			}
		}
	}
	return script.finalText
}

var idCounter int64
