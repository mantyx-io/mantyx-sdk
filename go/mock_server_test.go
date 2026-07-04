package mantyx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mockServer mirrors the subset of the MANTYX agent-runs HTTP surface used by
// the SDK tests. Each test instantiates a fresh `*mockServer` (one per test
// to avoid state bleed between table-driven cases).
type mockServer struct {
	srv *httptest.Server

	mu               sync.Mutex
	scriptForNextRun *runScript
	failAuth         bool
	// failScope, when non-nil, makes every route return 403
	// `insufficient_scope` with the configured `required` payload.
	// One element → string body; >1 elements → array body. Matches the
	// server's serialisation. See docs/agent-runs-protocol.md §2.3.
	failScope []string
	// failAuthCount, when > 0, makes the next N API requests return
	// 401; subsequent requests fall through to normal handling. Used
	// to exercise the SDK's "refresh + retry once on 401" flow.
	failAuthCount       int
	lastAuthHeader      string
	authHeaderHistory   []string
	lastToolResultBody  []byte
	lastRunCreateBody   []byte
	lastFeedbackBody    []byte
	lastFeedbackCreated bool
	feedbackByRun       map[string]struct {
		id   string
		body []byte
	}
	lastSessionCreateBody  []byte
	lastSessionMessageBody []byte
	lastEvalCreateBody     []byte
	evalRuns               map[string]map[string]any
	models                 ModelCatalog
	runs                   map[string]*runState
	sessions               map[string]*mockSession
	sessionScripts         map[string]*runScript

	// A2A test peer (served at /a2a/...).
	a2aAgentCard   map[string]any // GET /a2a/agent-card.json
	a2aReplyText   string         // text portion of POST /a2a/rpc reply
	lastA2ARequest []byte
	a2aAuthHeader  string

	// OAuth authorization server simulation.
	oauthAccessToken       string
	oauthRefreshToken      string
	oauthExpiresIn         int
	oauthScope             string
	oauthRotateAccessToken bool
	oauthNextError         *oauthMockError
	oauthTokenCallCount    int
	oauthLastTokenRequest  url.Values
	oauthRevokeCallCount   int
	oauthLastRevokeRequest url.Values
	oauthTokenLatency      time.Duration
	oauthTokenHook         func() // optional pre-response hook on /token (for single-flight tests)
}

type oauthMockError struct {
	Error       string
	Description string
	Status      int
}

type mockSession struct {
	messages  []Message
	metadata  map[string]string
	createdAt string
}

type runScript struct {
	events    []scriptEvent
	finalText string
}

type scriptEvent struct {
	kind string // "delta" | "result" | "local_tool_call"
	data map[string]any
	wait bool // for local_tool_call: pause until result posted
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
		sessions:       map[string]*mockSession{},
		sessionScripts: map[string]*runScript{},
		feedbackByRun: map[string]struct {
			id   string
			body []byte
		}{},
		evalRuns:               map[string]map[string]any{},
		oauthAccessToken:       "mantyx_at_mock_initial",
		oauthRefreshToken:      "mantyx_rt_mock_initial",
		oauthExpiresIn:         3600,
		oauthScope:             "models:read runs:write",
		oauthRotateAccessToken: true,
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
	auth := r.Header.Get("Authorization")
	m.lastAuthHeader = auth
	if auth != "" {
		m.authHeaderHistory = append(m.authHeaderHistory, auth)
	}
	m.mu.Unlock()
	// ── OAuth authorization server simulation ────────────────────────
	// Not gated by failAuth/failScope — these endpoints use their own
	// RFC 6749 error model (invalid_grant / invalid_client).
	if r.URL.Path == "/api/oauth/token" && r.Method == http.MethodPost {
		m.handleOAuthToken(w, r)
		return
	}
	if r.URL.Path == "/api/oauth/revoke" && r.Method == http.MethodPost {
		m.handleOAuthRevoke(w, r)
		return
	}
	m.mu.Lock()
	failAuth := m.failAuth
	consumeFailAuth := m.failAuthCount > 0
	if consumeFailAuth {
		m.failAuthCount--
	}
	failScope := m.failScope
	m.mu.Unlock()
	if failAuth || consumeFailAuth {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"Invalid API key or OAuth access token"}`)
		return
	}
	if failScope != nil {
		var requiredJSON []byte
		if len(failScope) == 1 {
			requiredJSON, _ = json.Marshal(failScope[0])
		} else {
			requiredJSON, _ = json.Marshal(failScope)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(
			"WWW-Authenticate",
			fmt.Sprintf(`Bearer error="insufficient_scope", scope=%q`, strings.Join(failScope, " ")),
		)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"error":"insufficient_scope","required":%s}`, string(requiredJSON)))
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
	case len(rest) >= 1 && rest[0] == "eval-datasets":
		m.handleEvalDatasets(w, r, rest[1:])
	case len(rest) >= 1 && rest[0] == "eval-runs":
		m.handleEvalRuns(w, r, rest[1:])
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
	case len(rest) == 2 && rest[1] == "feedback" && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		runID := rest[0]
		m.mu.Lock()
		m.lastFeedbackBody = raw
		existing, ok := m.feedbackByRun[runID]
		created := !ok
		m.lastFeedbackCreated = created
		id := existing.id
		if created {
			id = newID("fb")
		}
		m.feedbackByRun[runID] = struct {
			id   string
			body []byte
		}{id: id, body: raw}
		m.mu.Unlock()
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		m.writeJSON(w, status, map[string]any{
			"id":         id,
			"verdict":    body["verdict"],
			"targetKind": "agent_run",
			"agentRunId": runID,
		})
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
		var body struct {
			Metadata map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(raw, &body)
		meta := body.Metadata
		if meta == nil {
			meta = map[string]string{}
		}
		id := newID("sess")
		m.mu.Lock()
		m.sessions[id] = &mockSession{
			messages:  []Message{},
			metadata:  meta,
			createdAt: "2026-01-01T00:00:00.000Z",
		}
		m.mu.Unlock()
		m.writeJSON(w, http.StatusCreated, map[string]any{
			"sessionId": id,
			"name":      "ephemeral",
			"metadata":  meta,
			"createdAt": "2026-01-01T00:00:00.000Z",
		})
	case len(rest) == 0 && r.Method == http.MethodGet:
		filters := map[string]string{}
		for _, raw := range r.URL.Query()["metadata"] {
			idx := strings.Index(raw, ":")
			if idx <= 0 {
				http.Error(w, `{"error":"Invalid metadata filter"}`, http.StatusBadRequest)
				return
			}
			filters[raw[:idx]] = raw[idx+1:]
		}
		m.mu.Lock()
		summaries := []SessionSummary{}
		for id, sess := range m.sessions {
			matches := true
			for k, v := range filters {
				if sess.metadata[k] != v {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			summary := ""
			for _, msg := range sess.messages {
				if msg.Role == "user" {
					summary = msg.Content
					break
				}
			}
			summaries = append(summaries, SessionSummary{
				SessionID:           id,
				CreationDate:        sess.createdAt,
				LastInteractionDate: sess.createdAt,
				Summary:             summary,
				Metadata:            sess.metadata,
				Status:              "active",
			})
		}
		m.mu.Unlock()
		m.writeJSON(w, http.StatusOK, SessionListResult{
			Total:    len(summaries),
			Limit:    50,
			Offset:   0,
			Sessions: summaries,
		})
	case len(rest) == 2 && rest[1] == "events" && r.Method == http.MethodGet:
		m.mu.Lock()
		sess, ok := m.sessions[rest[0]]
		m.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"Session not found"}`, http.StatusNotFound)
			return
		}
		all := sess.messages
		selected := all
		full := r.URL.Query().Get("full") == "1" || r.URL.Query().Get("full") == "true"
		if !full {
			if v := r.URL.Query().Get("lastMessages"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && n < len(all) {
					selected = all[len(all)-n:]
				}
			}
		}
		startIndex := len(all) - len(selected)
		events := []map[string]any{}
		for i, msg := range selected {
			seq := startIndex + i + 1
			switch msg.Role {
			case "assistant":
				events = append(events, map[string]any{"seq": seq, "type": "assistant_message", "text": msg.Content})
			case "user":
				events = append(events, map[string]any{"seq": seq, "type": "user_message", "text": msg.Content})
			default:
				events = append(events, map[string]any{"seq": seq, "type": "message", "role": msg.Role, "text": msg.Content})
			}
		}
		m.writeJSON(w, http.StatusOK, map[string]any{
			"sessionId": rest[0],
			"total":     len(all),
			"events":    events,
		})
	case len(rest) == 1 && r.Method == http.MethodGet:
		m.mu.Lock()
		sess, ok := m.sessions[rest[0]]
		m.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"Session not found"}`, http.StatusNotFound)
			return
		}
		m.writeJSON(w, http.StatusOK, SessionInfo{
			ID:        rest[0],
			Name:      "ephemeral",
			Status:    "active",
			CreatedAt: sess.createdAt,
			Messages:  sess.messages,
			Metadata:  sess.metadata,
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
		m.sessions[sessionID].messages = append(m.sessions[sessionID].messages,
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

func (m *mockServer) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	rawBody, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(rawBody))
	m.mu.Lock()
	m.oauthTokenCallCount++
	m.oauthLastTokenRequest = form
	latency := m.oauthTokenLatency
	hook := m.oauthTokenHook
	nextErr := m.oauthNextError
	m.oauthNextError = nil
	access := m.oauthAccessToken
	refresh := m.oauthRefreshToken
	expiresIn := m.oauthExpiresIn
	scope := m.oauthScope
	rotate := m.oauthRotateAccessToken
	callIdx := m.oauthTokenCallCount
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if latency > 0 {
		time.Sleep(latency)
	}
	if nextErr != nil {
		status := nextErr.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		payload := map[string]string{"error": nextErr.Error}
		if nextErr.Description != "" {
			payload["error_description"] = nextErr.Description
		}
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	grant := form.Get("grant_type")
	if grant != "authorization_code" && grant != "refresh_token" && grant != "client_credentials" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"unsupported_grant_type"}`)
		return
	}
	accessToken := access
	if rotate {
		accessToken = fmt.Sprintf("%s_v%d", access, callIdx)
	}
	resp := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"scope":        valueOrDefault(form.Get("scope"), scope),
	}
	// refresh_token grant echoes back the same value the client sent
	// (non-rotating per docs/oauth.md). client_credentials never
	// returns one. authorization_code returns the persistent value.
	if grant == "refresh_token" {
		resp["refresh_token"] = valueOrDefault(form.Get("refresh_token"), refresh)
	} else if grant == "authorization_code" {
		resp["refresh_token"] = refresh
	}
	m.writeJSON(w, http.StatusOK, resp)
}

func (m *mockServer) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	rawBody, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(rawBody))
	m.mu.Lock()
	m.oauthRevokeCallCount++
	m.oauthLastRevokeRequest = form
	m.mu.Unlock()
	m.writeJSON(w, http.StatusOK, map[string]any{})
}

func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (m *mockServer) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	raw, _ := json.Marshal(body)
	_, _ = w.Write(raw)
}

func (m *mockServer) handleEvalDatasets(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, map[string]any{
			"datasets": []map[string]any{{
				"id": "ds_demo", "name": "Demo dataset", "description": nil,
				"caseCount": 1, "runCount": 0,
				"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
			}},
		})
	case len(rest) == 1 && r.Method == http.MethodGet:
		if rest[0] != "ds_demo" {
			http.NotFound(w, r)
			return
		}
		m.writeJSON(w, http.StatusOK, map[string]any{
			"id": "ds_demo", "name": "Demo dataset", "description": nil, "toolMocks": nil,
			"cases": []map[string]any{{
				"id": "case_1", "name": "hello",
				"input":   map[string]any{"role": "user", "content": "hi"},
				"scorers": []any{}, "tags": []any{}, "toolMocks": nil,
				"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
			}},
			"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
		})
	default:
		http.NotFound(w, r)
	}
}

func (m *mockServer) handleEvalRuns(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 1 && rest[0] == "compare" && r.Method == http.MethodGet:
		a := r.URL.Query().Get("a")
		b := r.URL.Query().Get("b")
		m.mu.Lock()
		runA, okA := m.evalRuns[a]
		runB, okB := m.evalRuns[b]
		m.mu.Unlock()
		if !okA || !okB {
			http.NotFound(w, r)
			return
		}
		m.writeJSON(w, http.StatusOK, map[string]any{
			"datasetId": runA["datasetId"], "datasetName": runA["datasetName"],
			"runA": runA, "runB": runB, "cases": []any{},
		})
	case len(rest) == 0 && r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastEvalCreateBody = raw
		m.mu.Unlock()
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		runID := newID("eval")
		detail := map[string]any{
			"id": runID, "datasetId": body["datasetId"], "datasetName": "Demo dataset",
			"agentId": body["agentId"], "inlineAgent": body["agent"] != nil,
			"status": "succeeded", "totalCases": 1, "completedCases": 1, "passedCases": 1,
			"score": 1.0, "tokenUsage": nil, "error": nil, "agentOverrides": body["overrides"],
			"startedAt": "2026-01-01T00:00:01Z", "finishedAt": "2026-01-01T00:00:02Z",
			"createdAt": "2026-01-01T00:00:00Z", "inlineAgentSpec": body["agent"],
			"agentSpecSnapshot": nil, "updatedAt": "2026-01-01T00:00:02Z", "results": []any{},
		}
		m.mu.Lock()
		m.evalRuns[runID] = detail
		m.mu.Unlock()
		m.writeJSON(w, http.StatusAccepted, map[string]string{
			"runId": runID, "status": "queued",
			"streamUrl": fmt.Sprintf("/api/v1/workspaces/x/eval-runs/%s/stream", runID),
		})
	case len(rest) == 0 && r.Method == http.MethodGet:
		m.mu.Lock()
		runs := make([]map[string]any, 0, len(m.evalRuns))
		for _, v := range m.evalRuns {
			runs = append(runs, v)
		}
		m.mu.Unlock()
		m.writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "limit": 50, "offset": 0})
	case len(rest) == 1 && r.Method == http.MethodGet:
		m.mu.Lock()
		detail, ok := m.evalRuns[rest[0]]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		m.writeJSON(w, http.StatusOK, detail)
	case len(rest) == 2 && rest[1] == "stream" && r.Method == http.MethodGet:
		m.mu.Lock()
		_, ok := m.evalRuns[rest[0]]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"snapshot\",\"status\":\"succeeded\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"run_completed\"}\n\n")
	case len(rest) == 2 && rest[1] == "cancel" && r.Method == http.MethodPost:
		m.mu.Lock()
		detail, ok := m.evalRuns[rest[0]]
		if ok {
			detail["status"] = "cancelled"
		}
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		m.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
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
