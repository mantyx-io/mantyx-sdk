package mantyx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL points at the public MANTYX API. Override via Options.BaseURL.
const DefaultBaseURL = "https://app.mantyx.io"

// Options configures a Client.
type Options struct {
	APIKey        string
	WorkspaceSlug string
	// BaseURL defaults to DefaultBaseURL when empty.
	BaseURL string
	// HTTPClient is used for all requests (one-shot HTTP and SSE). Defaults to
	// `&http.Client{Timeout: 0}` because SSE responses are long-lived; per-call
	// timeouts come from Context cancellation.
	HTTPClient *http.Client
}

// Client is the entry point of the SDK.
type Client struct {
	apiKey        string
	workspaceSlug string
	baseURL       string
	httpClient    *http.Client
}

// NewClient returns a configured Client. Panics on missing required fields.
func NewClient(opts Options) *Client {
	if opts.APIKey == "" {
		panic("mantyx: APIKey is required")
	}
	if opts.WorkspaceSlug == "" {
		panic("mantyx: WorkspaceSlug is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	return &Client{
		apiKey:        opts.APIKey,
		workspaceSlug: opts.WorkspaceSlug,
		baseURL:       opts.BaseURL,
		httpClient:    opts.HTTPClient,
	}
}

// ----- Models ---------------------------------------------------------------

// ModelCatalog is the response from ListModels.
type ModelCatalog struct {
	Models         []ModelInfo `json:"models"`
	DefaultModelID string      `json:"defaultModelId"`
}

// ModelInfo describes one selectable model.
type ModelInfo struct {
	ID                  string         `json:"id"`
	Label               string         `json:"label"`
	Provider            string         `json:"provider"`
	VendorModelID       string         `json:"vendorModelId"`
	Source              string         `json:"source"`
	ContextWindowTokens int            `json:"contextWindowTokens"`
	Pricing             *PricingInfo   `json:"pricing"`
}

// PricingInfo is best-effort and may be nil.
type PricingInfo struct {
	InputPer1MUsd     *float64 `json:"inputPer1MUsd,omitempty"`
	OutputPer1MUsd    *float64 `json:"outputPer1MUsd,omitempty"`
	CacheReadPer1MUsd *float64 `json:"cacheReadPer1MUsd,omitempty"`
}

// ListModels returns the catalog of selectable models for the configured
// workspace.
func (c *Client) ListModels(ctx context.Context) (ModelCatalog, error) {
	var out ModelCatalog
	err := c.do(ctx, "GET", "/models", nil, &out)
	return out, err
}

// ----- Run + session shared types -----------------------------------------

// Message is one entry in the conversation transcript.
type Message struct {
	Role    string `json:"role"` // user | assistant | system
	Content string `json:"content"`
}

// RunSpec describes a one-shot run.
type RunSpec struct {
	Name string
	// AgentID references a persisted MANTYX agent in this workspace. When set,
	// the server hydrates SystemPrompt, ModelID, and the agent's own tools
	// (memory, skills, plugin tools, …) from the Agent row at run time, and
	// any Tools you supply here are merged on top — typically LocalTool refs
	// you want the agent to be able to call back into.
	//
	// Either AgentID or SystemPrompt must be set.
	AgentID      string
	SystemPrompt string
	ModelID      string
	Tools        []ToolRef
	Prompt       string
	Messages     []Message
	// ReasoningLevel controls provider thinking strength on reasoning models.
	// Build one with ReasoningOff/Low/Medium/High or ReasoningEffort(n) where
	// n ∈ [0, 100]. Nil leaves the field unset (the server then falls back to
	// the agent's default — off for ephemeral specs, the persisted value for
	// AgentID-backed specs). See docs/agent-runs-protocol.md §4.4.
	ReasoningLevel *ReasoningLevel
	// Metadata is a flat string→string KV carried alongside the run for
	// observability. Visible (and filterable) in the MANTYX dashboard. Keys
	// must match `[A-Za-z0-9._-]{1,64}`, values are strings ≤ 256 chars, and
	// the map can have up to 16 entries.
	Metadata map[string]string
	// OnAssistantDelta is called once per assistant text chunk (best-effort).
	OnAssistantDelta func(string)
	// OnEvent is called for every event (assistant_delta, tool_result, ...).
	OnEvent func(RunEvent)
}

// SessionSpec describes the agent owned by a session.
type SessionSpec struct {
	Name string
	// AgentID references a persisted MANTYX agent in this workspace. See
	// RunSpec.AgentID for semantics. Either AgentID or SystemPrompt must be set.
	AgentID      string
	SystemPrompt string
	ModelID      string
	Tools        []ToolRef
	// ReasoningLevel sets the session-wide default applied to every run
	// created through Session.Send. See RunSpec.ReasoningLevel.
	ReasoningLevel *ReasoningLevel
	// Metadata is inherited by every run created through `Session.Send`. See
	// RunSpec.Metadata for the validation rules.
	Metadata map[string]string
}

// ReasoningLevel is provider thinking strength. Build one with the helpers
// below — its zero value is unusable; pass nil to leave the field unset.
type ReasoningLevel struct {
	raw any // string ("off"|"low"|"medium"|"high") or int (0..100)
}

// ReasoningOff disables provider thinking explicitly.
func ReasoningOff() *ReasoningLevel { return &ReasoningLevel{raw: "off"} }

// ReasoningLow snaps to the same anchor as the web composer's "Fast" preset.
func ReasoningLow() *ReasoningLevel { return &ReasoningLevel{raw: "low"} }

// ReasoningMedium snaps to the "Moderate" preset.
func ReasoningMedium() *ReasoningLevel { return &ReasoningLevel{raw: "medium"} }

// ReasoningHigh snaps to the "Smart" preset.
func ReasoningHigh() *ReasoningLevel { return &ReasoningLevel{raw: "high"} }

// ReasoningEffort accepts an explicit integer in [0, 100]. 0 explicitly
// disables provider thinking on reasoning models. Out-of-range values panic.
func ReasoningEffort(n int) *ReasoningLevel {
	if n < 0 || n > 100 {
		panic(fmt.Sprintf("mantyx.ReasoningEffort: %d is out of range [0, 100]", n))
	}
	return &ReasoningLevel{raw: n}
}

// MarshalJSON serialises the level to either a JSON string or a JSON number.
func (r *ReasoningLevel) MarshalJSON() ([]byte, error) {
	if r == nil || r.raw == nil {
		return []byte("null"), nil
	}
	return json.Marshal(r.raw)
}

// RunResult is the outcome of a successful run.
type RunResult struct {
	RunID  string
	Text   string
	Events []RunEvent
}

// RunEvent is one durable run event. Specific payload fields vary by Type.
type RunEvent struct {
	Seq  int                    `json:"seq"`
	Type string                 `json:"type"`
	Data map[string]any         `json:"-"`
}

// SessionInfo is the snapshot of a session row.
type SessionInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	CreatedAt  string            `json:"createdAt"`
	LastUsedAt string            `json:"lastUsedAt"`
	EndedAt    string            `json:"endedAt"`
	AgentSpec  map[string]any    `json:"agentSpec"`
	Messages   []Message         `json:"messages"`
	// Metadata that was attached to the session at create time.
	Metadata map[string]string `json:"metadata"`
}

// ----- One-shot run ---------------------------------------------------------

func (c *Client) RunAgent(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.AgentID == "" && spec.SystemPrompt == "" {
		return RunResult{}, &Error{Code: "invalid_request", Message: "either AgentID or SystemPrompt is required"}
	}
	if err := resolveLocalRefs(ctx, spec.Tools, c.httpClient); err != nil {
		return RunResult{}, err
	}
	defer closeMcpRefs(spec.Tools)
	body := serializeRunSpec(spec)
	created, err := c.createRun(ctx, "/agent-runs", body)
	if err != nil {
		return RunResult{}, err
	}
	return c.driveRun(ctx, created.RunID, spec.Tools, spec.OnAssistantDelta, spec.OnEvent)
}

// StreamAgent returns a channel that yields run events as they arrive. The
// channel is closed when the run terminates. Local-tool callbacks still run
// in the background; the SSE consumer drives them transparently.
func (c *Client) StreamAgent(ctx context.Context, spec RunSpec) (<-chan RunEvent, error) {
	if spec.AgentID == "" && spec.SystemPrompt == "" {
		return nil, &Error{Code: "invalid_request", Message: "either AgentID or SystemPrompt is required"}
	}
	if err := resolveLocalRefs(ctx, spec.Tools, c.httpClient); err != nil {
		return nil, err
	}
	body := serializeRunSpec(spec)
	created, err := c.createRun(ctx, "/agent-runs", body)
	if err != nil {
		closeMcpRefs(spec.Tools)
		return nil, err
	}
	ch := make(chan RunEvent, 32)
	go func() {
		defer close(ch)
		defer closeMcpRefs(spec.Tools)
		_, _ = c.consumeStream(ctx, created.RunID, collectLocalHandlers(spec.Tools), func(ev RunEvent) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		})
	}()
	return ch, nil
}

// ----- Sessions -------------------------------------------------------------

// CreateSession opens a new multi-turn session and returns a Session handle.
func (c *Client) CreateSession(ctx context.Context, spec SessionSpec) (*Session, error) {
	if spec.AgentID == "" && spec.SystemPrompt == "" {
		return nil, &Error{Code: "invalid_request", Message: "either AgentID or SystemPrompt is required"}
	}
	if err := resolveLocalRefs(ctx, spec.Tools, c.httpClient); err != nil {
		return nil, err
	}
	body := map[string]any{
		"tools": toolWire(spec.Tools),
	}
	if spec.SystemPrompt != "" {
		body["systemPrompt"] = spec.SystemPrompt
	}
	if spec.AgentID != "" {
		body["agentId"] = spec.AgentID
	}
	if spec.Name != "" {
		body["name"] = spec.Name
	}
	if spec.ModelID != "" {
		body["modelId"] = spec.ModelID
	}
	if spec.ReasoningLevel != nil {
		body["reasoningLevel"] = spec.ReasoningLevel
	}
	if len(spec.Metadata) > 0 {
		body["metadata"] = spec.Metadata
	}
	var resp struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
	}
	if err := c.do(ctx, "POST", "/agent-sessions", body, &resp); err != nil {
		closeMcpRefs(spec.Tools)
		return nil, err
	}
	return &Session{
		ID:       resp.SessionID,
		client:   c,
		handlers: collectLocalHandlers(spec.Tools),
		tools:    spec.Tools,
	}, nil
}

// ResumeSession returns a Session handle for an existing id. If `tools` is
// non-nil, the SDK refreshes the server's tool snapshot (and re-binds local
// handlers) on the next `Send` call.
func (c *Client) ResumeSession(ctx context.Context, id string, tools []ToolRef) (*Session, error) {
	if _, err := c.GetSessionInfo(ctx, id); err != nil {
		return nil, err
	}
	if err := resolveLocalRefs(ctx, tools, c.httpClient); err != nil {
		return nil, err
	}
	return &Session{
		ID:        id,
		client:    c,
		handlers:  collectLocalHandlers(tools),
		toolsWire: toolWire(tools),
		tools:     tools,
	}, nil
}

// EndSession marks the session terminal. Future `Send` calls return 409.
func (c *Client) EndSession(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/agent-sessions/"+pathEscape(id), nil, nil)
}

// GetSessionInfo returns a snapshot of the session row.
func (c *Client) GetSessionInfo(ctx context.Context, id string) (SessionInfo, error) {
	var out SessionInfo
	err := c.do(ctx, "GET", "/agent-sessions/"+pathEscape(id), nil, &out)
	return out, err
}

// ----- Run driver -----------------------------------------------------------

type createRunResponse struct {
	RunID     string `json:"runId"`
	StreamURL string `json:"streamUrl"`
}

func (c *Client) createRun(ctx context.Context, path string, body map[string]any) (createRunResponse, error) {
	var out createRunResponse
	err := c.do(ctx, "POST", path, body, &out)
	return out, err
}

// driveRun walks the SSE stream to completion and returns the final RunResult.
func (c *Client) driveRun(
	ctx context.Context,
	runID string,
	tools []ToolRef,
	onDelta func(string),
	onEvent func(RunEvent),
) (RunResult, error) {
	return c.driveRunWithRegistry(ctx, runID, collectLocalHandlers(tools), onDelta, onEvent)
}

// driveRunWithRegistry is the lower-level entry point — used by Session
// where the registry is already pre-built.
func (c *Client) driveRunWithRegistry(
	ctx context.Context,
	runID string,
	handlers *localToolRegistry,
	onDelta func(string),
	onEvent func(RunEvent),
) (RunResult, error) {
	collected := make([]RunEvent, 0, 32)
	finalText := ""
	terminalErr, err := c.consumeStream(ctx, runID, handlers, func(ev RunEvent) {
		collected = append(collected, ev)
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Type == "assistant_delta" && onDelta != nil {
			if t, ok := ev.Data["text"].(string); ok {
				onDelta(t)
			}
		}
		if ev.Type == "result" {
			if t, ok := ev.Data["text"].(string); ok {
				finalText = t
			}
		}
	})
	if err != nil {
		return RunResult{}, err
	}
	if terminalErr != nil {
		return RunResult{}, terminalErr
	}
	return RunResult{RunID: runID, Text: finalText, Events: collected}, nil
}

// consumeStream opens the SSE stream, dispatches local tools, and notifies
// the caller via `onEvent`. It returns a non-nil RunError when the run ended
// in `error`/`cancelled`. Network errors are returned as a wrapped error.
func (c *Client) consumeStream(
	ctx context.Context,
	runID string,
	handlers *localToolRegistry,
	onEvent func(RunEvent),
) (terminalErr error, fatal error) {
	lastSeq := 0
	for {
		path := fmt.Sprintf("/agent-runs/%s/stream", pathEscape(runID))
		if lastSeq > 0 {
			path = fmt.Sprintf("%s?lastSeq=%d", path, lastSeq)
		}
		req, err := c.newRequest(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		if lastSeq > 0 {
			req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", lastSeq))
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return &RunError{RunID: runID, Code: "cancelled", Message: ctx.Err().Error()}, nil
			}
			return nil, &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, c.errorFromResponse(resp)
		}

		var sawTerminal bool
		readErr := readSseStream(resp.Body, func(ev SseEvent) bool {
			if ctx.Err() != nil {
				return false
			}
			data := map[string]any{}
			if ev.Data != "" {
				_ = json.Unmarshal([]byte(ev.Data), &data)
			}
			seq := lastSeq
			if v, ok := data["seq"].(float64); ok {
				seq = int(v)
				if seq > lastSeq {
					lastSeq = seq
				}
			}
			evType := ev.Event
			if evType == "" {
				if t, ok := data["type"].(string); ok {
					evType = t
				}
			}
			runEv := RunEvent{Seq: seq, Type: evType, Data: data}
			onEvent(runEv)

			switch evType {
			case "local_tool_call":
				go c.dispatchLocalTool(ctx, runID, runEv, handlers)
			case "result":
				sawTerminal = true
				if subtype, _ := data["subtype"].(string); subtype != "success" && subtype != "" {
					msg, _ := data["error"].(string)
					if msg == "" {
						msg = subtype
					}
					terminalErr = &RunError{RunID: runID, Code: subtype, Message: msg}
				}
				return false
			case "error":
				sawTerminal = true
				msg, _ := data["error"].(string)
				code, _ := data["code"].(string)
				terminalErr = &RunError{RunID: runID, Code: code, Message: msg}
				return false
			case "cancelled":
				sawTerminal = true
				terminalErr = &RunError{RunID: runID, Code: "cancelled", Message: "Run was cancelled"}
				return false
			}
			return true
		})
		resp.Body.Close()
		if sawTerminal {
			return terminalErr, nil
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return &RunError{RunID: runID, Code: "cancelled", Message: ctx.Err().Error()}, nil
			}
			// Reconnect after a tiny backoff.
			select {
			case <-ctx.Done():
				return &RunError{RunID: runID, Code: "cancelled", Message: ctx.Err().Error()}, nil
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		// Stream closed cleanly without a terminal event — reconnect.
	}
}

func (c *Client) dispatchLocalTool(ctx context.Context, runID string, ev RunEvent, handlers *localToolRegistry) {
	name, _ := ev.Data["name"].(string)
	toolUseID, _ := ev.Data["toolUseId"].(string)
	if toolUseID == "" {
		return
	}
	kind, _ := ev.Data["kind"].(string)
	if kind == "" {
		kind = "local"
	}
	switch kind {
	case "a2a_local":
		tool, ok := handlers.a2aTools[name]
		if !ok {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", fmt.Sprintf("No local A2A handler registered for tool %q", name))
			return
		}
		message := ""
		if args, ok := ev.Data["args"].(map[string]any); ok {
			if m, ok := args["message"].(string); ok {
				message = m
			}
		}
		out, err := callA2A(ctx, tool, message, c.httpClient)
		if err != nil {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", err.Error())
			return
		}
		_ = c.PostToolResult(ctx, runID, toolUseID, out, "")
	case "mcp_local":
		serverName, _ := ev.Data["mcpServer"].(string)
		mcpToolName, _ := ev.Data["mcpToolName"].(string)
		server, ok := handlers.mcpServers[serverName]
		if !ok {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", fmt.Sprintf("No local MCP server registered for %q", serverName))
			return
		}
		server.mu.Lock()
		r := server.resolved
		server.mu.Unlock()
		if r == nil || r.callTool == nil {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", fmt.Sprintf("Local MCP server %q has not been resolved", serverName))
			return
		}
		upstream, ok := r.upstreamNames[mcpToolName]
		if !ok {
			// Fall back to stripping the server prefix in case the wire echoes
			// a tool we didn't ship in our `tools/list` snapshot.
			upstream = strings.TrimPrefix(mcpToolName, server.spec.Name+"_")
		}
		rawArgs, _ := json.Marshal(ev.Data["args"])
		out, err := r.callTool(ctx, upstream, rawArgs)
		if err != nil {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", err.Error())
			return
		}
		_ = c.PostToolResult(ctx, runID, toolUseID, out, "")
	default:
		tool, ok := handlers.localTools[name]
		if !ok {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", fmt.Sprintf("No local handler registered for tool %q", name))
			return
		}
		rawArgs, _ := json.Marshal(ev.Data["args"])
		out, err := tool.spec.Execute(ctx, rawArgs)
		if err != nil {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", err.Error())
			return
		}
		_ = c.PostToolResult(ctx, runID, toolUseID, out, "")
	}
}

// PostToolResult sends the SDK's response for a `local_tool_call` event back to
// the server. Either `result` (success) or `errMsg` (failure) should be set.
func (c *Client) PostToolResult(ctx context.Context, runID, toolUseID, result, errMsg string) error {
	body := map[string]any{"toolUseId": toolUseID}
	if result != "" {
		body["result"] = result
	}
	if errMsg != "" {
		body["error"] = errMsg
	}
	path := fmt.Sprintf("/agent-runs/%s/tool-results", pathEscape(runID))
	return c.do(ctx, "POST", path, body, nil)
}

// CancelRun aborts a run server-side. The run row's status moves to
// "cancelled" and a `cancelled` event is appended to its event log.
func (c *Client) CancelRun(ctx context.Context, runID string) error {
	path := fmt.Sprintf("/agent-runs/%s/cancel", pathEscape(runID))
	return c.do(ctx, "POST", path, nil, nil)
}

// ----- HTTP plumbing --------------------------------------------------------

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	url := c.baseURL + "/api/v1/workspaces/" + pathEscape(c.workspaceSlug) + path
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.errorFromResponse(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	body2, err := io.ReadAll(resp.Body)
	if err != nil {
		return &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
	}
	if len(body2) == 0 {
		return nil
	}
	if err := json.Unmarshal(body2, out); err != nil {
		return &Error{Message: "invalid JSON response: " + err.Error(), Code: "invalid_response"}
	}
	return nil
}

func (c *Client) errorFromResponse(resp *http.Response) error {
	body := struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Hint  string `json:"hint"`
	}{}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	msg := body.Error
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	base := &Error{Message: msg, Code: body.Code, HTTPStatus: resp.StatusCode, Hint: body.Hint}
	if resp.StatusCode == http.StatusUnauthorized {
		return &AuthError{Inner: base}
	}
	if base.Code == "" {
		base.Code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return base
}

// ----- helpers --------------------------------------------------------------

func serializeRunSpec(spec RunSpec) map[string]any {
	body := map[string]any{
		"tools": toolWire(spec.Tools),
	}
	if spec.SystemPrompt != "" {
		body["systemPrompt"] = spec.SystemPrompt
	}
	if spec.AgentID != "" {
		body["agentId"] = spec.AgentID
	}
	if spec.Name != "" {
		body["name"] = spec.Name
	}
	if spec.ModelID != "" {
		body["modelId"] = spec.ModelID
	}
	if spec.ReasoningLevel != nil {
		body["reasoningLevel"] = spec.ReasoningLevel
	}
	if spec.Prompt != "" {
		body["prompt"] = spec.Prompt
	}
	if len(spec.Messages) > 0 {
		body["messages"] = spec.Messages
	}
	if len(spec.Metadata) > 0 {
		body["metadata"] = spec.Metadata
	}
	return body
}

func pathEscape(s string) string {
	// Tight URL-path escaping that keeps simple alphanumerics intact while
	// rejecting anything that would break the `/api/v1/.../<id>` shape.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", r)
	}
	return b.String()
}
