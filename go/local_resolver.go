package mantyx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveLocalRefs performs the side effects required to ship `a2a_local` and
// `mcp_local` tools over the wire: fetching A2A Agent Cards and opening MCP
// transports + running Initialize/tools/list. Resolved state is cached on
// the ToolRef itself, so subsequent calls (e.g. Session.Send) are cheap.
//
// httpClient is used for both the A2A fetch and any per-tool HTTPClient
// override fall-through.
func resolveLocalRefs(ctx context.Context, tools []ToolRef, httpClient *http.Client) error {
	for _, t := range tools {
		switch r := t.(type) {
		case *localA2ATool:
			if err := resolveA2A(ctx, r, httpClient); err != nil {
				return err
			}
		case *localMcpServer:
			if err := resolveMcp(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// closeMcpRefs releases the transports of every `mcp_local` ToolRef that
// resolveLocalRefs has opened so far. Idempotent — safe to call multiple
// times. Returned errors are joined into a single error.
func closeMcpRefs(tools []ToolRef) error {
	var firstErr error
	for _, t := range tools {
		s, ok := t.(*localMcpServer)
		if !ok {
			continue
		}
		s.mu.Lock()
		r := s.resolved
		s.resolved = nil
		s.mu.Unlock()
		if r == nil || r.close == nil {
			continue
		}
		if err := r.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ---------- A2A ------------------------------------------------------------

func resolveA2A(ctx context.Context, t *localA2ATool, httpClient *http.Client) error {
	t.mu.Lock()
	if t.resolvedCard != nil {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()

	client := t.spec.HTTPClient
	if client == nil {
		client = httpClient
	}
	if client == nil {
		client = http.DefaultClient
	}
	card, err := fetchAgentCard(ctx, client, t.spec.AgentCardURL, t.spec.Headers)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.resolvedCard = card
	t.mu.Unlock()
	return nil
}

// fetchAgentCard performs the GET, validates the response is a JSON object
// with at least a `name` field, and returns it as-is so the wire round-trip
// preserves any peer-specific extensions.
func fetchAgentCard(ctx context.Context, client *http.Client, url string, headers map[string]string) (map[string]any, error) {
	if url == "" {
		return nil, fmt.Errorf("mantyx.LocalA2A: AgentCardURL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("mantyx.LocalA2A: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mantyx.LocalA2A: fetching %s: %w", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("mantyx.LocalA2A: reading %s: %w", url, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mantyx.LocalA2A: %s returned %d: %s", url, res.StatusCode, string(body))
	}
	var card map[string]any
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("mantyx.LocalA2A: %s returned invalid JSON: %w", url, err)
	}
	name, _ := card["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("mantyx.LocalA2A: %s returned an Agent Card without a `name`", url)
	}
	return card, nil
}

// callA2A POSTs an A2A `message/send` JSON-RPC request to the resolved
// Agent Card's `url` and returns the flattened text reply.
func callA2A(ctx context.Context, t *localA2ATool, message string, httpClient *http.Client) (string, error) {
	t.mu.Lock()
	card := t.resolvedCard
	t.mu.Unlock()
	if card == nil {
		return "", fmt.Errorf("mantyx.LocalA2A: Agent Card has not been resolved for tool %q", t.spec.Name)
	}
	rpcURL, _ := card["url"].(string)
	if rpcURL == "" {
		// Some peers publish the JSON-RPC endpoint at the same URL as the card.
		rpcURL = t.spec.AgentCardURL
	}
	client := t.spec.HTTPClient
	if client == nil {
		client = httpClient
	}
	if client == nil {
		client = http.DefaultClient
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      randomID(),
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": randomID(),
				"role":      "user",
				"parts":     []any{map[string]any{"kind": "text", "text": message}},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("mantyx.LocalA2A: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("mantyx.LocalA2A: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mantyx.LocalA2A: POST %s: %w", rpcURL, err)
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("mantyx.LocalA2A: read response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("mantyx.LocalA2A: %s returned %d: %s", rpcURL, res.StatusCode, string(respBody))
	}
	var envelope map[string]any
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", fmt.Errorf("mantyx.LocalA2A: invalid JSON response: %w", err)
	}
	if errObj, ok := envelope["error"].(map[string]any); ok {
		msg, _ := errObj["message"].(string)
		if msg == "" {
			msg = "remote error"
		}
		return "", fmt.Errorf("mantyx.LocalA2A: %s: %s", t.spec.Name, msg)
	}
	return extractA2AReplyText(envelope["result"]), nil
}

// extractA2AReplyText walks the JSON-RPC result and concatenates every text
// `Part` it finds. The A2A spec is permissive — `result` may be either a
// `Message` (with `parts`) or a `Task` whose `status.message` carries the
// reply, possibly with a list of artifacts of their own.
func extractA2AReplyText(result any) string {
	if result == nil {
		return ""
	}
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	if parts, ok := m["parts"].([]any); ok {
		if t := textFromParts(parts); t != "" {
			return t
		}
	}
	if status, ok := m["status"].(map[string]any); ok {
		if msg, ok := status["message"].(map[string]any); ok {
			if parts, ok := msg["parts"].([]any); ok {
				if t := textFromParts(parts); t != "" {
					return t
				}
			}
		}
	}
	if arts, ok := m["artifacts"].([]any); ok {
		var b strings.Builder
		for _, a := range arts {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			if parts, ok := am["parts"].([]any); ok {
				if t := textFromParts(parts); t != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(t)
				}
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}

func textFromParts(parts []any) string {
	var b strings.Builder
	for _, p := range parts {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := m["kind"].(string); kind != "" && kind != "text" {
			continue
		}
		if t, ok := m["text"].(string); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t)
		}
	}
	return b.String()
}

// ---------- MCP ------------------------------------------------------------

func resolveMcp(ctx context.Context, s *localMcpServer) error {
	s.mu.Lock()
	if s.resolved != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	transport, cleanup, err := buildMcpTransport(ctx, s.spec)
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mantyx-go-sdk", Version: Version()}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if cleanup != nil {
			_ = cleanup()
		}
		return fmt.Errorf("mantyx.LocalMcp[%s]: connect: %w", s.spec.Name, err)
	}
	listing, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		if cleanup != nil {
			_ = cleanup()
		}
		return fmt.Errorf("mantyx.LocalMcp[%s]: tools/list: %w", s.spec.Name, err)
	}
	wireTools := make([]map[string]any, 0, len(listing.Tools))
	upstream := make(map[string]string, len(listing.Tools))
	for _, t := range listing.Tools {
		entry, err := mcpToolToWire(t, s.spec.Name)
		if err != nil {
			_ = session.Close()
			if cleanup != nil {
				_ = cleanup()
			}
			return fmt.Errorf("mantyx.LocalMcp[%s]: encode tool %q: %w", s.spec.Name, t.Name, err)
		}
		wireTools = append(wireTools, entry)
		upstream[entry["name"].(string)] = t.Name
	}
	var serverInfo map[string]any
	if init := session.InitializeResult(); init != nil && init.ServerInfo != nil {
		raw, err := json.Marshal(init.ServerInfo)
		if err == nil {
			_ = json.Unmarshal(raw, &serverInfo)
		}
	}

	r := &resolvedMcp{
		serverInfo:    serverInfo,
		tools:         wireTools,
		upstreamNames: upstream,
		callTool: func(ctx context.Context, toolName string, args json.RawMessage) (string, error) {
			var argMap any
			if len(args) > 0 {
				if err := json.Unmarshal(args, &argMap); err != nil {
					return "", fmt.Errorf("mantyx.LocalMcp[%s]: invalid args: %w", s.spec.Name, err)
				}
			}
			res, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: argMap,
			})
			if err != nil {
				return "", err
			}
			if res.IsError {
				return "", fmt.Errorf("mantyx.LocalMcp[%s]: %s: %s", s.spec.Name, toolName, flattenMcpText(res))
			}
			return flattenMcpText(res), nil
		},
		close: func() error {
			err := session.Close()
			if cleanup != nil {
				if cerr := cleanup(); cerr != nil && err == nil {
					err = cerr
				}
			}
			return err
		},
	}
	s.mu.Lock()
	s.resolved = r
	s.mu.Unlock()
	return nil
}

// buildMcpTransport returns an MCP transport (Streamable HTTP or stdio)
// configured for spec, plus an optional cleanup function for transport-side
// resources that aren't tied to the session lifecycle (currently unused but
// reserved so future transports — e.g. SSH-tunneled stdio — have a hook).
func buildMcpTransport(_ context.Context, spec LocalMcpSpec) (mcp.Transport, func() error, error) {
	if spec.URL != "" {
		client := spec.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		if len(spec.Headers) > 0 {
			client = &http.Client{
				Transport: headerInjectingTransport{
					base:    client.Transport,
					headers: spec.Headers,
				},
				Timeout: client.Timeout,
			}
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   spec.URL,
			HTTPClient: client,
		}, nil, nil
	}
	if spec.Command == "" {
		return nil, nil, fmt.Errorf("mantyx.LocalMcp[%s]: neither URL nor Command set", spec.Name)
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	if len(spec.Env) > 0 {
		cmd.Env = append([]string{}, os.Environ()...)
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}
	return &mcp.CommandTransport{Command: cmd}, nil, nil
}

// headerInjectingTransport wraps a base RoundTripper to set fixed headers on
// every outbound request — used so per-server custom Headers reach the
// Streamable HTTP MCP endpoint without having to subclass the SDK's transport.
type headerInjectingTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	return base.RoundTrip(clone)
}

// mcpToolToWire serializes a *mcp.Tool into the JSON object the wire
// protocol expects, prefixing the tool name with the server's Name (unless
// the upstream tool already starts with that prefix).
func mcpToolToWire(t *mcp.Tool, serverName string) (map[string]any, error) {
	if t == nil {
		return nil, fmt.Errorf("nil tool entry")
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if name, ok := out["name"].(string); ok && name != "" {
		out["name"] = prefixedMcpToolName(serverName, name)
	}
	if _, has := out["inputSchema"]; !has {
		out["inputSchema"] = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return out, nil
}

// flattenMcpText collapses every TextContent block in an MCP CallToolResult
// into a single newline-joined string. Non-text blocks are ignored — the
// MANTYX wire protocol expects a single string per `tool_result`.
func flattenMcpText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "id"
	}
	return hex.EncodeToString(b[:])
}
