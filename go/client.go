package mantyx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL points at the public MANTYX API. Override via Options.BaseURL.
const DefaultBaseURL = "https://app.mantyx.io"

// Options configures a Client.
//
// Exactly one of APIKey / AccessToken must be set:
//
//   - APIKey — a workspace API key (token prefix `mantyx_…`).
//   - AccessToken — a MANTYX OAuth 2.0 access token (token prefix
//     `mantyx_at_…`).
//
// The server resolves either kind by token-prefix sniffing, so the SDK
// only ships one `Authorization: Bearer <credential>` header. Exposing
// both option names makes call sites self-documenting; passing both
// returns an `invalid_request` Error.
//
// See `docs/agent-runs-protocol.md` §2 for the full credential table,
// scope semantics, and the `insufficient_scope` 403 surfaced as
// *ScopeError.
type Options struct {
	// APIKey is a workspace API key (token prefix `mantyx_…`). Mutually
	// exclusive with AccessToken / TokenSource; see the Options doc.
	APIKey string
	// AccessToken is a MANTYX OAuth 2.0 access token (token prefix
	// `mantyx_at_…`). Mutually exclusive with APIKey / TokenSource;
	// see the Options doc. OAuth tokens additionally enforce per-route
	// scopes (`runs:read`, `runs:write`, `sessions:read`,
	// `sessions:write`, `models:read`, `mantyx.identity:read`);
	// missing scopes surface as *ScopeError.
	//
	// Static access tokens live 1 hour per `docs/oauth.md` §"Token
	// lifetimes & lifecycle" — for long-running processes prefer
	// TokenSource.
	AccessToken string
	// TokenSource is a dynamic credential provider that the SDK calls
	// before every request, and again with reason=ReasonUnauthorized
	// after a 401 (refresh + retry once). Build one with
	// (*OAuthClient).RefreshTokenSource or
	// (*OAuthClient).ClientCredentialsTokenSource, or supply any
	// implementation of TokenSource for full custom control (e.g.
	// tokens minted by an upstream auth proxy).
	//
	// Mutually exclusive with APIKey / AccessToken.
	TokenSource   TokenSource
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
	tokenSource   TokenSource
	workspaceSlug string
	baseURL       string
	httpClient    *http.Client
}

// NewClient returns a configured Client. Panics on missing required fields.
func NewClient(opts Options) *Client {
	credential, source := resolveCredential(opts)
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
		apiKey:        credential,
		tokenSource:   source,
		workspaceSlug: opts.WorkspaceSlug,
		baseURL:       opts.BaseURL,
		httpClient:    opts.HTTPClient,
	}
}

// resolveCredential picks exactly one of APIKey / AccessToken /
// TokenSource and returns (credential, source). Mixing options panics.
func resolveCredential(opts Options) (string, TokenSource) {
	var provided []string
	if opts.APIKey != "" {
		provided = append(provided, "APIKey")
	}
	if opts.AccessToken != "" {
		provided = append(provided, "AccessToken")
	}
	if opts.TokenSource != nil {
		provided = append(provided, "TokenSource")
	}
	if len(provided) > 1 {
		panic("mantyx: pass exactly one of Options.APIKey, Options.AccessToken, or Options.TokenSource — got " + strings.Join(provided, " + "))
	}
	if len(provided) == 0 {
		panic("mantyx: one of Options.APIKey (workspace API key), Options.AccessToken (OAuth access token), or Options.TokenSource (dynamic credential provider) is required")
	}
	credential := opts.APIKey
	if credential == "" {
		credential = opts.AccessToken
	}
	return credential, opts.TokenSource
}

// ----- Models ---------------------------------------------------------------

// ModelCatalog is the response from ListModels.
type ModelCatalog struct {
	Models         []ModelInfo `json:"models"`
	DefaultModelID string      `json:"defaultModelId"`
}

// ModelInfo describes one selectable model.
type ModelInfo struct {
	ID                  string       `json:"id"`
	Label               string       `json:"label"`
	Provider            string       `json:"provider"`
	VendorModelID       string       `json:"vendorModelId"`
	Source              string       `json:"source"`
	ContextWindowTokens int          `json:"contextWindowTokens"`
	Pricing             *PricingInfo `json:"pricing"`
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
	Role        string           `json:"role"` // user | assistant | system
	Content     string           `json:"content"`
	Attachments []map[string]any `json:"attachments,omitempty"`
}

// InputFileAttachment builds an inline file attachment for the last user
// message in a run. See docs/agent-runs-protocol.md §4.0.1.
func InputFileAttachment(mimeType, filename, data string) map[string]any {
	return map[string]any{
		"type":     "input_file",
		"mimeType": mimeType,
		"filename": filename,
		"data":     data,
	}
}

// InputFileURLAttachment builds a URL file attachment for the last user
// message in a run.
func InputFileURLAttachment(url string, mimeType, filename string) map[string]any {
	out := map[string]any{
		"type": "input_file_url",
		"url":  url,
	}
	if mimeType != "" {
		out["mimeType"] = mimeType
	}
	if filename != "" {
		out["filename"] = filename
	}
	return out
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
	// Attachments is shorthand for a single user turn with file inputs. When
	// Prompt is set and Messages is empty, the SDK builds a one-entry
	// Messages slice. Ignored when Messages is non-empty.
	Attachments []map[string]any
	// ReasoningLevel controls provider thinking strength on reasoning models.
	// Build one with ReasoningOff/Low/Medium/High or ReasoningEffort(n) where
	// n ∈ [0, 100]. Nil leaves the field unset (the server then falls back to
	// the agent's default — off for ephemeral specs, the persisted value for
	// AgentID-backed specs). See docs/agent-runs-protocol.md §4.4.
	ReasoningLevel *ReasoningLevel
	// OutputSchema constrains the model's final assistant text to a JSON
	// document matching a JSON Schema. Use ParseRunOutput on the returned
	// RunResult to JSON-decode the reply (and optionally validate against
	// your own type). See OutputSchema and docs/wire-protocol.md §7.
	OutputSchema *OutputSchema
	// LoopDetection configures the loop-detection guard: when MANTYX sees
	// `consecutiveThreshold` identical (toolName, args) batches in a row it
	// injects a steering nudge ("either deliver a final answer or change
	// strategy"); after `hardCutoffThreshold` it forces a tools-disabled
	// finalise turn. Build with LoopDetectionThresholds(...) or pass the
	// sentinel LoopDetectionDisabled() to opt out for this run. nil leaves
	// the field unset (the runtime defaults apply: 3 / 6).
	// See docs/agent-runs-protocol.md §4.6.
	LoopDetection *LoopDetection
	// ToolBudgets caps how many times each tool may execute over the
	// lifetime of the run. Calls past the cap are intercepted before
	// execution and the model receives a synthetic "budget exceeded —
	// pivot or finalize" tool result. Pass an empty map to clear the
	// runtime defaults; omit to keep them. See docs/agent-runs-protocol.md §4.7.
	ToolBudgets ToolBudgets
	// Supervisor configures the optional platform LLM run judge. Build with
	// SupervisorInterval(...) or pass SupervisorDisabled() to opt out for
	// this run. nil leaves the field unset (the runtime default applies on
	// ephemeral API runs: enabled with interval 5). See
	// docs/agent-runs-protocol.md §4.8.
	Supervisor *Supervisor
	// Plan turns on the in-product task plan (live checklist + optional
	// plan-only termination). Build with PlanAuto, PlanRequired,
	// PlanWithSteps, PlanOnly, or PlanDisabled. nil leaves the field unset.
	// See `docs/agent-runs-protocol.md` §4.9. Prefer RunPlan for plan-only
	// runs.
	Plan *Plan
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
	// OutputSchema sets the session-wide default applied to every run
	// created through Session.Send. See RunSpec.OutputSchema.
	OutputSchema *OutputSchema
	// LoopDetection sets the session-wide default applied to every run
	// created through Session.Send. See RunSpec.LoopDetection.
	LoopDetection *LoopDetection
	// ToolBudgets sets the session-wide default applied to every run
	// created through Session.Send. See RunSpec.ToolBudgets.
	ToolBudgets ToolBudgets
	// Supervisor sets the session-wide default applied to every run created
	// through Session.Send. See RunSpec.Supervisor.
	Supervisor *Supervisor
	// Plan sets the session-wide default applied to every run created
	// through Session.Send. See RunSpec.Plan.
	Plan *Plan
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

// OutputSchema asks the provider to constrain the model's final assistant
// text to a JSON document matching a JSON Schema. The terminal `result` event
// still carries the reply as `Text: string`. Set Enforcement to
// OutputSchemaEnforcementStrict when provider rejection or unconstrained
// fallback must fail. Use ParseRunOutput to JSON-decode the result.
//
// Name (optional, default "output") is forwarded to the provider as the
// stable schema identifier (OpenAI `text.format.name`, Anthropic synthetic
// tool name). Must match `/^[a-zA-Z0-9_-]{1,64}$/` when set.
//
// Schema describes the assistant text. Its root must be a JSON object —
// most providers reject array / scalar roots in structured-output mode.
// Schema is one of:
//
//   - map[string]any / json.RawMessage     → passed through as-is
//   - a Go struct (or pointer-to-struct)   → reflected to JSON Schema via
//     google/jsonschema-go (the same path as LocalToolSpec.Parameters; use
//     the `jsonschema:"..."` struct tag to attach per-field descriptions)
//
// The resolved schema is shipped verbatim; MANTYX does not validate its
// contents (the provider does).
//
// See `docs/wire-protocol.md` §7 for the full per-provider mapping.
type OutputSchema struct {
	Name        string
	Schema      any
	Enforcement OutputSchemaEnforcement
}

// OutputSchemaEnforcement controls how strictly the platform must constrain
// the final assistant output. The zero value preserves best-effort behavior.
type OutputSchemaEnforcement string

const (
	OutputSchemaEnforcementBestEffort OutputSchemaEnforcement = "best_effort"
	OutputSchemaEnforcementStrict     OutputSchemaEnforcement = "strict"
)

const outputSchemaMaxBytes = 32 * 1024

var outputSchemaNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// resolve returns the JSON-Schema map for s.Schema, reflecting Go
// structs through jsonSchemaFor when needed. Callers see a typed
// `invalid_request` Error if Schema is missing or unrepresentable.
func (s *OutputSchema) resolve() (map[string]any, error) {
	if s == nil || s.Schema == nil {
		return nil, &Error{
			Code:    "invalid_request",
			Message: "OutputSchema.Schema is required (the JSON Schema root must be a JSON object)",
		}
	}
	schema, err := jsonSchemaFor(s.Schema)
	if err != nil {
		return nil, &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf("OutputSchema.Schema cannot be reflected to JSON Schema: %v", err),
		}
	}
	return schema, nil
}

// MarshalJSON serialises OutputSchema to its wire shape, reflecting any
// non-map Schema input through jsonSchemaFor first.
func (s *OutputSchema) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	schema, err := s.resolve()
	if err != nil {
		return nil, err
	}
	out := map[string]any{"schema": schema}
	if s.Name != "" {
		out["name"] = s.Name
	}
	if s.Enforcement != "" {
		out["enforcement"] = s.Enforcement
	}
	return json.Marshal(out)
}

// validate mirrors the server-side `400 invalid_request` checks (name regex,
// schema shape, ≤ 32 KB serialised) so callers see a typed Go error rather
// than a round-trip rejection.
func (s *OutputSchema) validate() error {
	if s == nil {
		return nil
	}
	if s.Name != "" && !outputSchemaNameRE.MatchString(s.Name) {
		return &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf("OutputSchema.Name must match /^[a-zA-Z0-9_-]{1,64}$/, got %q", s.Name),
		}
	}
	if s.Enforcement != "" &&
		s.Enforcement != OutputSchemaEnforcementBestEffort &&
		s.Enforcement != OutputSchemaEnforcementStrict {
		return &Error{
			Code: "invalid_request",
			Message: fmt.Sprintf(
				"OutputSchema.Enforcement must be %q or %q, got %q",
				OutputSchemaEnforcementBestEffort,
				OutputSchemaEnforcementStrict,
				s.Enforcement,
			),
		}
	}
	if _, err := s.resolve(); err != nil {
		return err
	}
	enc, err := json.Marshal(s)
	if err != nil {
		return &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf("OutputSchema is not JSON-serialisable: %v", err),
		}
	}
	if len(enc) > outputSchemaMaxBytes {
		return &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf("OutputSchema serialised JSON is %d bytes; the server enforces a 32 KB limit", len(enc)),
		}
	}
	return nil
}

// LoopDetection configures the loop-detection guard. The pipeline tracks
// an order-invariant `(toolName, args)` signature for every assistant turn
// that emits one or more tool calls; when the same signature repeats
// `ConsecutiveThreshold` rounds in a row MANTYX injects a steering nudge
// ("either deliver a final answer or change strategy"); after
// `HardCutoffThreshold` rounds it forces a tools-disabled finalise turn.
//
// Both fields are optional. Omitted ones inherit the runtime defaults
// (`ConsecutiveThreshold: 3`, `HardCutoffThreshold: 6`). Build a value
// through LoopDetectionThresholds(consecutive, hardCutoff) for the typed
// builder, or LoopDetectionDisabled() to opt the run out of the guard.
//
// See `docs/agent-runs-protocol.md` §4.6.
type LoopDetection struct {
	// ConsecutiveThreshold is the number of identical consecutive batches
	// that triggers the **soft nudge**. Default 3. Must be >= 2.
	// Server-side upper bound: 100. 0 leaves the field unset (the runtime
	// default applies).
	ConsecutiveThreshold int
	// HardCutoffThreshold is the number of identical consecutive batches
	// that triggers the **hard cutoff** (forced tools-disabled finalise
	// turn). Default 6. Must be strictly greater than ConsecutiveThreshold.
	// Server-side upper bound: 100. 0 leaves the field unset.
	HardCutoffThreshold int

	disabled bool
}

// LoopDetectionThresholds builds a LoopDetection with the supplied
// thresholds. Pass 0 for either field to leave it unset (the runtime
// default is then used by the server). Out-of-range values panic.
func LoopDetectionThresholds(consecutive, hardCutoff int) *LoopDetection {
	ld := &LoopDetection{
		ConsecutiveThreshold: consecutive,
		HardCutoffThreshold:  hardCutoff,
	}
	if err := ld.validate(); err != nil {
		panic("mantyx.LoopDetectionThresholds: " + err.Error())
	}
	return ld
}

// LoopDetectionDisabled returns a LoopDetection sentinel that disables
// the guard for the run / session it is attached to.
func LoopDetectionDisabled() *LoopDetection {
	return &LoopDetection{disabled: true}
}

const loopDetectionThresholdMax = 100

// validate mirrors the server-side `400 invalid_request` checks.
func (l *LoopDetection) validate() error {
	if l == nil || l.disabled {
		return nil
	}
	if l.ConsecutiveThreshold != 0 {
		if l.ConsecutiveThreshold < 2 {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("LoopDetection.ConsecutiveThreshold must be >= 2, got %d", l.ConsecutiveThreshold)}
		}
		if l.ConsecutiveThreshold > loopDetectionThresholdMax {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("LoopDetection.ConsecutiveThreshold must be <= %d, got %d", loopDetectionThresholdMax, l.ConsecutiveThreshold)}
		}
	}
	if l.HardCutoffThreshold != 0 {
		if l.HardCutoffThreshold < 3 {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("LoopDetection.HardCutoffThreshold must be >= 3, got %d", l.HardCutoffThreshold)}
		}
		if l.HardCutoffThreshold > loopDetectionThresholdMax {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("LoopDetection.HardCutoffThreshold must be <= %d, got %d", loopDetectionThresholdMax, l.HardCutoffThreshold)}
		}
	}
	if l.ConsecutiveThreshold != 0 && l.HardCutoffThreshold != 0 &&
		l.HardCutoffThreshold <= l.ConsecutiveThreshold {
		return &Error{Code: "invalid_request", Message: fmt.Sprintf("LoopDetection.HardCutoffThreshold (%d) must be strictly greater than LoopDetection.ConsecutiveThreshold (%d)", l.HardCutoffThreshold, l.ConsecutiveThreshold)}
	}
	return nil
}

// MarshalJSON serialises LoopDetection to its wire shape: either the
// literal `false` (when built via LoopDetectionDisabled), or an object
// carrying any explicitly-set thresholds.
func (l *LoopDetection) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("null"), nil
	}
	if l.disabled {
		return []byte("false"), nil
	}
	out := map[string]any{}
	if l.ConsecutiveThreshold != 0 {
		out["consecutiveThreshold"] = l.ConsecutiveThreshold
	}
	if l.HardCutoffThreshold != 0 {
		out["hardCutoffThreshold"] = l.HardCutoffThreshold
	}
	return json.Marshal(out)
}

// Supervisor configures the optional platform LLM run judge. Build with
// SupervisorInterval(n) for a custom review interval, or
// SupervisorDisabled() to opt the run out of the judge. nil leaves the
// field unset (the runtime default applies on ephemeral API runs).
type Supervisor struct {
	// Interval is the number of LLM calls between supervisor reviews.
	// Default 5 when the supervisor is enabled and Interval is 0 (unset).
	// Server-side upper bound: 100.
	Interval int

	// ReasoningTrigger configures mid-turn reasoning reviews. nil leaves
	// the field unset (server default: 3000 chars / 30s). Set
	// reasoningTriggerDisabled to send the literal `false`.
	ReasoningTrigger *ReasoningTrigger

	reasoningTriggerDisabled bool
	disabled                 bool
}

// ReasoningTrigger configures mid-turn supervisor reviews while a single
// turn is still streaming reasoning.
type ReasoningTrigger struct {
	// Chars is the reasoning span length before review. Default 3000 when
	// unset. Server-side upper bound: 50000.
	Chars int
	// Ms is the reasoning span duration in milliseconds before review.
	// Default 30000 when unset. Server-side upper bound: 600000.
	Ms int
}

// DisableReasoningTrigger returns a copy of s with mid-turn reasoning
// reviews disabled (wire shape `reasoningTrigger: false`).
func (s *Supervisor) DisableReasoningTrigger() *Supervisor {
	if s == nil {
		s = &Supervisor{}
	}
	out := *s
	out.reasoningTriggerDisabled = true
	out.ReasoningTrigger = nil
	return &out
}

// SupervisorInterval builds a Supervisor with the supplied review interval.
// Pass 0 to leave interval unset (the server default of 5 applies). Values
// outside [1, 100] panic.
func SupervisorInterval(n int) *Supervisor {
	s := &Supervisor{Interval: n}
	if err := s.validate(); err != nil {
		panic("mantyx.SupervisorInterval: " + err.Error())
	}
	return s
}

// SupervisorDisabled returns a Supervisor sentinel that disables the platform
// run judge for the run / session it is attached to.
func SupervisorDisabled() *Supervisor {
	return &Supervisor{disabled: true}
}

const supervisorIntervalMax = 100
const supervisorReasoningCharsMax = 50000
const supervisorReasoningMsMax = 600000

func (s *Supervisor) validate() error {
	if s == nil || s.disabled {
		return nil
	}
	if s.Interval != 0 {
		if s.Interval < 1 {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.Interval must be >= 1, got %d", s.Interval)}
		}
		if s.Interval > supervisorIntervalMax {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.Interval must be <= %d, got %d", supervisorIntervalMax, s.Interval)}
		}
	}
	if s.ReasoningTrigger != nil {
		if s.ReasoningTrigger.Chars != 0 {
			if s.ReasoningTrigger.Chars < 1 {
				return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.ReasoningTrigger.Chars must be >= 1, got %d", s.ReasoningTrigger.Chars)}
			}
			if s.ReasoningTrigger.Chars > supervisorReasoningCharsMax {
				return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.ReasoningTrigger.Chars must be <= %d, got %d", supervisorReasoningCharsMax, s.ReasoningTrigger.Chars)}
			}
		}
		if s.ReasoningTrigger.Ms != 0 {
			if s.ReasoningTrigger.Ms < 1 {
				return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.ReasoningTrigger.Ms must be >= 1, got %d", s.ReasoningTrigger.Ms)}
			}
			if s.ReasoningTrigger.Ms > supervisorReasoningMsMax {
				return &Error{Code: "invalid_request", Message: fmt.Sprintf("Supervisor.ReasoningTrigger.Ms must be <= %d, got %d", supervisorReasoningMsMax, s.ReasoningTrigger.Ms)}
			}
		}
	}
	return nil
}

// MarshalJSON serialises Supervisor to its wire shape: either the literal
// `false` (when built via SupervisorDisabled), or an object carrying any
// explicitly-set interval.
func (s *Supervisor) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	if s.disabled {
		return []byte("false"), nil
	}
	out := map[string]any{}
	if s.Interval != 0 {
		out["interval"] = s.Interval
	}
	if s.reasoningTriggerDisabled {
		out["reasoningTrigger"] = false
	} else if s.ReasoningTrigger != nil {
		rt := map[string]any{}
		if s.ReasoningTrigger.Chars != 0 {
			rt["chars"] = s.ReasoningTrigger.Chars
		}
		if s.ReasoningTrigger.Ms != 0 {
			rt["ms"] = s.ReasoningTrigger.Ms
		}
		out["reasoningTrigger"] = rt
	}
	return json.Marshal(out)
}

// TaskPlanStepStatus is the lifecycle state of one checklist row.
type TaskPlanStepStatus string

const (
	TaskPlanPending    TaskPlanStepStatus = "pending"
	TaskPlanInProgress TaskPlanStepStatus = "in_progress"
	TaskPlanDone       TaskPlanStepStatus = "done"
	TaskPlanBlocked    TaskPlanStepStatus = "blocked"
	TaskPlanSkipped    TaskPlanStepStatus = "skipped"
)

// TaskPlanStep is one row in an in-product task plan.
type TaskPlanStep struct {
	ID     string             `json:"id,omitempty"`
	Title  string             `json:"title"`
	Status TaskPlanStepStatus `json:"status"`
}

// TaskPlan is the structured checklist on `task_plan` events and
// plan-only terminal results. See `docs/agent-runs-protocol.md` §4.9.
type TaskPlan struct {
	V        int            `json:"v,omitempty"`
	PlanID   string         `json:"planId,omitempty"`
	Revision int            `json:"revision,omitempty"`
	Mode     string         `json:"mode,omitempty"`
	Source   string         `json:"source,omitempty"`
	Brief    string         `json:"brief,omitempty"`
	Steps    []TaskPlanStep `json:"steps"`
}

// TaskPlanTransition is one transition in a v2 `task_plan` event.
type TaskPlanTransition struct {
	Kind   string `json:"kind"`
	StepID string `json:"stepId,omitempty"`
}

type planWireMode int

const (
	planWireUnset planWireMode = iota
	planWireAuto
	planWireRequired
	planWireDisabled
	planWireObject
)

// Plan configures the in-product task plan. Build with PlanAuto,
// PlanRequired, PlanWithSteps, PlanOnly, or PlanDisabled. nil leaves the
// field unset (no planning). See `docs/agent-runs-protocol.md` §4.9.
type Plan struct {
	mode     planWireMode
	PlanOnly bool
	Mode     string
	Brief    string
	Steps    []string
}

// PlanAuto exposes update_task_plan; the agent decides during its first
// turn whether multi-step tracking is useful.
func PlanAuto() *Plan { return &Plan{mode: planWireAuto} }

// PlanRequired requires update_task_plan before substantive tools.
func PlanRequired() *Plan { return &Plan{mode: planWireRequired} }

// PlanDisabled explicitly disables planning for the run / session.
func PlanDisabled() *Plan { return &Plan{mode: planWireDisabled} }

// PlanWithSteps seeds a caller-provided checklist and tracks step statuses
// during execution.
func PlanWithSteps(steps ...string) *Plan {
	return &Plan{mode: planWireObject, Steps: steps}
}

// PlanOnly produces the plan and terminates without executing the agent
// loop. Omit steps to let the one-shot planner decide; pass steps for a
// caller-provided checklist.
func PlanOnly(steps ...string) *Plan {
	return &Plan{mode: planWireObject, PlanOnly: true, Steps: steps}
}

// WithBrief returns a copy of the plan with an optional one-line objective.
func (p *Plan) WithBrief(brief string) *Plan {
	if p == nil {
		return &Plan{mode: planWireObject, PlanOnly: true, Brief: brief}
	}
	cp := *p
	cp.Brief = brief
	return &cp
}

// WithMode returns a copy with an explicit planning mode (`off`, `auto`, or
// `required`). Only applies to object-form plans.
func (p *Plan) WithMode(mode string) *Plan {
	if p == nil {
		return &Plan{mode: planWireObject, Mode: mode}
	}
	cp := *p
	cp.mode = planWireObject
	cp.Mode = mode
	return &cp
}

func (p *Plan) validate() error {
	if p == nil || p.mode != planWireObject {
		return nil
	}
	if p.Mode != "" && p.Mode != "off" && p.Mode != "auto" && p.Mode != "required" {
		return &Error{Code: "invalid_request", Message: fmt.Sprintf(`Plan.Mode must be "off", "auto", or "required", got %q`, p.Mode)}
	}
	for i, step := range p.Steps {
		if step == "" {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("Plan.Steps[%d] must be a non-empty string", i)}
		}
	}
	return nil
}

// MarshalJSON serialises Plan to its wire shape: `true`, `"auto"`,
// `"required"`, `false`, or `{ mode?, planOnly?, brief?, steps? }`.
func (p *Plan) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	switch p.mode {
	case planWireAuto:
		return []byte("true"), nil
	case planWireRequired:
		return []byte(`"required"`), nil
	case planWireDisabled:
		return []byte("false"), nil
	default:
		out := map[string]any{}
		if p.PlanOnly {
			out["planOnly"] = true
		}
		if p.Mode != "" {
			out["mode"] = p.Mode
		}
		if p.Brief != "" {
			out["brief"] = p.Brief
		}
		if len(p.Steps) > 0 {
			out["steps"] = p.Steps
		}
		return json.Marshal(out)
	}
}

// ToolBudget caps how many times one tool may execute over the run.
type ToolBudget struct {
	// MaxCalls is the hard cap on executed calls per run. 0 disables the
	// tool entirely (every attempt returns the synthetic "budget exceeded"
	// body on the first try). Server-side upper bound: 1000.
	MaxCalls int `json:"maxCalls"`
}

// ToolBudgets is the per-tool call-cap map. Keys are model-facing tool
// names (the same string the model sees on a tool call); values are
// ToolBudget structs. Pass an empty (non-nil) map to start from a clean
// slate (no runtime defaults applied on top); leave the field nil to
// keep the runtime defaults. See `docs/agent-runs-protocol.md` §4.7.
type ToolBudgets map[string]ToolBudget

const (
	toolBudgetsMaxEntries = 32
	toolBudgetMaxNameLen  = 120
	toolBudgetMaxCalls    = 1000
)

// validate mirrors the server-side `400 invalid_request` checks.
func (b ToolBudgets) validate() error {
	if b == nil {
		return nil
	}
	if len(b) > toolBudgetsMaxEntries {
		return &Error{Code: "invalid_request", Message: fmt.Sprintf("ToolBudgets has %d entries; the server enforces a %d-entry limit", len(b), toolBudgetsMaxEntries)}
	}
	for name, entry := range b {
		if len(name) < 1 || len(name) > toolBudgetMaxNameLen {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("ToolBudgets keys must be 1..%d-char strings, got %q", toolBudgetMaxNameLen, name)}
		}
		if entry.MaxCalls < 0 {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("ToolBudgets[%q].MaxCalls must be a non-negative integer, got %d", name, entry.MaxCalls)}
		}
		if entry.MaxCalls > toolBudgetMaxCalls {
			return &Error{Code: "invalid_request", Message: fmt.Sprintf("ToolBudgets[%q].MaxCalls must be <= %d (server-enforced), got %d", name, toolBudgetMaxCalls, entry.MaxCalls)}
		}
	}
	return nil
}

// RunTokenUsage carries per-run token totals aggregated across every
// model invocation for the run. Attached to terminal `result` / `error`
// events (and to `GET /agent-runs/:runId`) by MANTYX ≥ 2026-09. Older
// runners omit the cost-attribution triple entirely; SDK callers detect
// "no usage data" by checking `result.Model == nil` or
// `result.Model.Provider == ""`.
//
// See `docs/agent-runs-protocol.md` §7.1 for the per-provider mapping
// and the relationship between buckets — `InputTokens` / `OutputTokens`
// are the billable totals; `CachedTokens` and `ReasoningTokens` are
// diagnostic breakdowns _inside_ those two totals, not separate
// additive buckets.
type RunTokenUsage struct {
	// InputTokens is the total billable input — fresh prompt tokens
	// plus the cached-read slice the provider still bills (at a
	// discount) plus any cache-creation tokens plus tool-prompt tokens.
	InputTokens int `json:"inputTokens"`
	// CachedTokens is the discounted slice of `InputTokens` that came
	// from a prompt cache hit (Anthropic prompt caching, OpenAI cached
	// prompt, Gemini implicit cache). 0 when the provider doesn't
	// report cache reads or the run didn't hit cache.
	CachedTokens int `json:"cachedTokens"`
	// ReasoningTokens is the non-visible thinking-token slice. Already
	// counted inside `OutputTokens`; surfaced separately so dashboards
	// can break out "thinking cost" vs visible output. 0 when the
	// model didn't reason or didn't report it.
	ReasoningTokens int `json:"reasoningTokens"`
	// OutputTokens is the total tokens the model emitted for this run
	// (visible + reasoning). Matches the provider's "completion
	// tokens" / "output tokens" billing line.
	OutputTokens int `json:"outputTokens"`
}

// RunModelInfo identifies the resolved model that actually executed
// the run. Surfaced on terminal events (and `GET /agent-runs/:runId`)
// by MANTYX ≥ 2026-09. See `docs/agent-runs-protocol.md` §7.1.
type RunModelInfo struct {
	// ID is the catalog id — the same string a caller would pass back
	// as `ModelID` to re-select this exact entry (e.g. `"platform:demo"`,
	// `"provider:cmf…"`). Empty against legacy fallbacks that didn't
	// synthesise a catalog id.
	ID string `json:"id"`
	// Provider is the lowercase provider id (`"openai"`, `"anthropic"`,
	// `"google"`, `"azure-openai"`). Empty against legacy runners that
	// don't report usage data — that's the "no usage data" sentinel
	// when `RunResult.Model` is non-nil.
	Provider string `json:"provider"`
	// VendorModelID is the model id the platform actually sent to the
	// provider (e.g. `"gpt-5.4-mini"`, `"claude-opus-4-7"`,
	// `"gemini-2.5-pro"`).
	VendorModelID string `json:"vendorModelId"`
	// ReasoningEffort is `"off" | "low" | "medium" | "high"`. Empty
	// when the provider doesn't expose a reasoning-level knob or the
	// run didn't request one.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// StructuredOutputEnforcementMechanism identifies how the platform
// constrained a structured-output run.
type StructuredOutputEnforcementMechanism string

const (
	StructuredOutputEnforcementNativeSchema  StructuredOutputEnforcementMechanism = "native_schema"
	StructuredOutputEnforcementSyntheticTool StructuredOutputEnforcementMechanism = "synthetic_tool"
	StructuredOutputEnforcementNone          StructuredOutputEnforcementMechanism = "none"
)

// StructuredOutputInfo is terminal observability metadata for output-schema
// enforcement.
type StructuredOutputInfo struct {
	SchemaRequested               bool                                 `json:"schemaRequested"`
	SchemaEnforced                bool                                 `json:"schemaEnforced"`
	EnforcementMechanism          StructuredOutputEnforcementMechanism `json:"enforcementMechanism"`
	UnconstrainedFallbackOccurred bool                                 `json:"unconstrainedFallbackOccurred"`
}

// RunResult is the outcome of a successful run.
type RunResult struct {
	RunID  string
	Text   string
	Events []RunEvent
	// Tokens carries per-run token totals from the terminal event.
	// nil against MANTYX servers older than 2026-09 (the "no usage
	// data" signal). See RunTokenUsage and
	// `docs/agent-runs-protocol.md` §7.1.
	Tokens *RunTokenUsage
	// Turns is the total `engine.completeTurn(...)` invocations for
	// the run, including the failing call when a run errored mid-loop.
	// A single-shot run reports 1; a tool loop is >= 2. 0 against
	// legacy MANTYX servers.
	Turns int
	// Model identifies the resolved model that executed the run. nil
	// against legacy MANTYX servers. See RunModelInfo.
	Model *RunModelInfo
	// StructuredOutput carries output-schema enforcement metadata. nil
	// against legacy servers and runs without terminal observability data.
	StructuredOutput *StructuredOutputInfo
	// Plan carries the final structured checklist for plan-only runs.
	// nil for normal executed runs — use `task_plan` events for live
	// progress. See `docs/agent-runs-protocol.md` §4.9.
	Plan *TaskPlan
}

// RunEvent is one durable run event. Specific payload fields vary by Type.
type RunEvent struct {
	Seq  int            `json:"seq"`
	Type string         `json:"type"`
	Data map[string]any `json:"-"`
}

// SessionInfo is the snapshot of a session row.
type SessionInfo struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	CreatedAt  string         `json:"createdAt"`
	LastUsedAt string         `json:"lastUsedAt"`
	EndedAt    string         `json:"endedAt"`
	AgentSpec  map[string]any `json:"agentSpec"`
	Messages   []Message      `json:"messages"`
	// Metadata that was attached to the session at create time.
	Metadata map[string]string `json:"metadata"`
}

// SessionSummary is one row from ListSessions.
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	// CreationDate is an ISO 8601 timestamp.
	CreationDate string `json:"creationDate"`
	// LastInteractionDate is an ISO 8601 timestamp of the most recent run.
	LastInteractionDate string `json:"lastInteractionDate"`
	// Summary is a best-effort label derived from the first user prompt
	// (sessions have no title).
	Summary  string            `json:"summary"`
	Metadata map[string]string `json:"metadata"`
	Status   string            `json:"status"`
}

// SessionListResult is the paginated response from ListSessions.
type SessionListResult struct {
	Total      int              `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
	NextCursor string           `json:"nextCursor"`
	Sessions   []SessionSummary `json:"sessions"`
}

// ListSessionsOptions filters and paginates a ListSessions call. The zero
// value lists every session in the workspace.
type ListSessionsOptions struct {
	// Metadata filters to sessions whose stored metadata contains every
	// supplied key/value pair (AND-combined server-side). Use the same
	// identifiers you attached at create time.
	Metadata map[string]string
	// Status optionally filters by lifecycle ("active" | "ended").
	Status string
	// Limit caps the page size (server default 50, max 200). 0 leaves it unset.
	Limit int
	// Offset skips the first N rows for pagination. 0 leaves it unset.
	Offset int
	// Cursor is the opaque NextCursor returned by the previous page.
	Cursor string
}

// GetSessionEventsOptions controls how much of a session's conversation
// GetSessionEvents returns. The zero value returns the full history.
type GetSessionEventsOptions struct {
	// Full returns every message frame (the default behaviour).
	Full bool
	// LastMessages, when > 0 and Full is false, returns only the most recent
	// N message frames.
	LastMessages int
}

// SessionEventsPageOptions controls a bounded session-history read.
type SessionEventsPageOptions struct {
	// LastMessages is the page size (server max 500). Defaults to 100.
	LastMessages int
	// BeforeSeq pages backward from an earlier message sequence.
	BeforeSeq int
}

// SessionEventsPage is one bounded page of session event frames.
type SessionEventsPage struct {
	SessionID     string
	Total         int
	Events        []RunEvent
	NextBeforeSeq int
	Truncated     bool
}

// ----- One-shot run ---------------------------------------------------------

func (c *Client) RunAgent(ctx context.Context, spec RunSpec) (RunResult, error) {
	if !agentIdentityPresent(spec.AgentID, spec.SystemPrompt, spec.Messages, spec.Attachments, spec.Prompt) {
		return RunResult{}, &Error{Code: "invalid_request", Message: "either AgentID, SystemPrompt, or a non-empty system message in Messages is required"}
	}
	if err := spec.OutputSchema.validate(); err != nil {
		return RunResult{}, err
	}
	if err := spec.LoopDetection.validate(); err != nil {
		return RunResult{}, err
	}
	if err := spec.ToolBudgets.validate(); err != nil {
		return RunResult{}, err
	}
	if err := spec.Supervisor.validate(); err != nil {
		return RunResult{}, err
	}
	if err := spec.Plan.validate(); err != nil {
		return RunResult{}, err
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

// RunPlanSpec is sugar for a plan-only RunAgent call.
type RunPlanSpec struct {
	RunSpec
	// Steps, when non-empty, supplies a caller-provided checklist. Omit to
	// let the one-shot planner decide (plan-only compatibility path).
	Steps []string
	// Brief is an optional one-line objective for a caller-provided plan.
	Brief string
}

// RunPlan classifies (or accepts caller Steps) and returns the structured
// checklist without executing the agent loop. Equivalent to RunAgent with
// Plan: PlanOnly(...).
func (c *Client) RunPlan(ctx context.Context, spec RunPlanSpec) (RunResult, error) {
	p := PlanOnly(spec.Steps...)
	if spec.Brief != "" {
		p = p.WithBrief(spec.Brief)
	}
	spec.RunSpec.Plan = p
	return c.RunAgent(ctx, spec.RunSpec)
}

// StreamAgent returns a channel that yields run events as they arrive. The
// channel is closed when the run terminates. Local-tool callbacks still run
// in the background; the SSE consumer drives them transparently.
func (c *Client) StreamAgent(ctx context.Context, spec RunSpec) (<-chan RunEvent, error) {
	if spec.AgentID == "" && spec.SystemPrompt == "" {
		return nil, &Error{Code: "invalid_request", Message: "either AgentID or SystemPrompt is required"}
	}
	if err := spec.OutputSchema.validate(); err != nil {
		return nil, err
	}
	if err := spec.LoopDetection.validate(); err != nil {
		return nil, err
	}
	if err := spec.ToolBudgets.validate(); err != nil {
		return nil, err
	}
	if err := spec.Supervisor.validate(); err != nil {
		return nil, err
	}
	if err := spec.Plan.validate(); err != nil {
		return nil, err
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
	if err := spec.OutputSchema.validate(); err != nil {
		return nil, err
	}
	if err := spec.LoopDetection.validate(); err != nil {
		return nil, err
	}
	if err := spec.ToolBudgets.validate(); err != nil {
		return nil, err
	}
	if err := spec.Supervisor.validate(); err != nil {
		return nil, err
	}
	if err := spec.Plan.validate(); err != nil {
		return nil, err
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
	if spec.OutputSchema != nil {
		body["outputSchema"] = spec.OutputSchema
	}
	if spec.LoopDetection != nil {
		body["loopDetection"] = spec.LoopDetection
	}
	if spec.ToolBudgets != nil {
		body["toolBudgets"] = serializeToolBudgets(spec.ToolBudgets)
	}
	if spec.Supervisor != nil {
		body["supervisor"] = spec.Supervisor
	}
	if spec.Plan != nil {
		body["plan"] = spec.Plan
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

// ListSessions returns the workspace's sessions, most-recently-used first.
// Filter by the metadata you attached at create time to find earlier sessions
// by your own identifiers (customer id, environment, …). Multiple metadata
// entries are AND-combined server-side.
func (c *Client) ListSessions(ctx context.Context, opts ListSessionsOptions) (SessionListResult, error) {
	q := url.Values{}
	for k, v := range opts.Metadata {
		q.Add("metadata", k+":"+v)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	path := "/agent-sessions"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out SessionListResult
	err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}

// GetSessionEvents fetches a session's conversation as realtime-style event
// frames so a UI can restore the thread through the same handler it uses for
// the live stream. Returns `user_message` / `assistant_message` frames (see
// the wire protocol §6.2). Pass opts.LastMessages to fetch only the most
// recent turns, or opts.Full for the entire history (the default).
func (c *Client) GetSessionEvents(ctx context.Context, id string, opts GetSessionEventsOptions) ([]RunEvent, error) {
	q := url.Values{}
	if opts.Full {
		q.Set("full", "1")
	} else if opts.LastMessages > 0 {
		q.Set("lastMessages", strconv.Itoa(opts.LastMessages))
	}
	path := "/agent-sessions/" + pathEscape(id) + "/events"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		SessionID string           `json:"sessionId"`
		Total     int              `json:"total"`
		Events    []map[string]any `json:"events"`
	}
	if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return decodeSessionEventFrames(resp.Events), nil
}

// GetSessionEventsPage fetches one bounded page in ascending sequence order.
// Follow NextBeforeSeq to walk backward through older history.
func (c *Client) GetSessionEventsPage(ctx context.Context, id string, opts SessionEventsPageOptions) (SessionEventsPage, error) {
	q := url.Values{}
	pageSize := opts.LastMessages
	if pageSize <= 0 {
		pageSize = 100
	}
	q.Set("lastMessages", strconv.Itoa(pageSize))
	if opts.BeforeSeq > 0 {
		q.Set("beforeSeq", strconv.Itoa(opts.BeforeSeq))
	}
	path := "/agent-sessions/" + pathEscape(id) + "/events?" + q.Encode()
	var resp struct {
		SessionID     string           `json:"sessionId"`
		Total         int              `json:"total"`
		Events        []map[string]any `json:"events"`
		NextBeforeSeq int              `json:"nextBeforeSeq"`
		Truncated     bool             `json:"truncated"`
	}
	if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
		return SessionEventsPage{}, err
	}
	return SessionEventsPage{
		SessionID:     resp.SessionID,
		Total:         resp.Total,
		Events:        decodeSessionEventFrames(resp.Events),
		NextBeforeSeq: resp.NextBeforeSeq,
		Truncated:     resp.Truncated,
	}, nil
}

func decodeSessionEventFrames(frames []map[string]any) []RunEvent {
	events := make([]RunEvent, 0, len(frames))
	for i, frame := range frames {
		seq := i + 1
		if v, ok := frame["seq"].(float64); ok {
			seq = int(v)
		}
		evType, _ := frame["type"].(string)
		if evType == "" {
			evType = "message"
		}
		data := make(map[string]any, len(frame))
		for k, v := range frame {
			if k == "seq" || k == "type" {
				continue
			}
			data[k] = v
		}
		events = append(events, RunEvent{Seq: seq, Type: evType, Data: data})
	}
	return events
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
	// Cost-attribution triple captured from the terminal `result`
	// event. Left zero/nil against legacy MANTYX servers — callers
	// detect "no usage data" via `result.Model == nil`. See
	// `docs/agent-runs-protocol.md` §7.1.
	var tokens *RunTokenUsage
	var turns int
	var modelInfo *RunModelInfo
	var structuredOutput *StructuredOutputInfo
	var taskPlan *TaskPlan
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
			if t := parseRunTokens(ev.Data["tokens"]); t != nil {
				tokens = t
			}
			if n, ok := parseRunTurns(ev.Data["turns"]); ok {
				turns = n
			}
			if m := parseRunModel(ev.Data["model"]); m != nil {
				modelInfo = m
			}
			if info := parseStructuredOutputInfo(ev.Data["structuredOutput"]); info != nil {
				structuredOutput = info
			}
			if p := parseTaskPlan(ev.Data["plan"]); p != nil {
				taskPlan = p
			}
		}
	})
	if err != nil {
		return RunResult{}, err
	}
	if terminalErr != nil {
		return RunResult{}, terminalErr
	}
	return RunResult{
		RunID:            runID,
		Text:             finalText,
		Events:           collected,
		Tokens:           tokens,
		Turns:            turns,
		Model:            modelInfo,
		StructuredOutput: structuredOutput,
		Plan:             taskPlan,
	}, nil
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
		// At-most-one refresh + retry on 401 for the initial SSE open
		// when a TokenSource is configured. Mid-stream 401s drop into
		// the network-blip reconnect path below.
		resp, err := c.openSSEStream(ctx, path, lastSeq)
		if err != nil {
			if ctx.Err() != nil {
				return &RunError{RunID: runID, Code: "cancelled", Message: ctx.Err().Error()}, nil
			}
			return nil, err
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
			payload := runEventPayload(data)
			seq := lastSeq
			if v, ok := payload["seq"].(float64); ok {
				seq = int(v)
				if seq > lastSeq {
					lastSeq = seq
				}
			} else if v, ok := data["seq"].(float64); ok {
				seq = int(v)
				if seq > lastSeq {
					lastSeq = seq
				}
			}
			evType := ev.Event
			if evType == "" {
				if t, ok := payload["type"].(string); ok {
					evType = t
				} else if t, ok := data["type"].(string); ok {
					evType = t
				}
			}
			runEv := RunEvent{Seq: seq, Type: evType, Data: payload}
			onEvent(runEv)

			switch evType {
			case "local_tool_call":
				go c.dispatchLocalTool(ctx, runID, runEv, handlers)
			case "result":
				sawTerminal = true
				if subtype, _ := payload["subtype"].(string); subtype != "success" && subtype != "" {
					msg, _ := payload["error"].(string)
					if msg == "" {
						msg = subtype
					}
					terminalErr = &RunError{
						RunID:            runID,
						Code:             subtype,
						Message:          msg,
						StructuredOutput: parseStructuredOutputInfo(payload["structuredOutput"]),
					}
				}
				return false
			case "error":
				sawTerminal = true
				msg, _ := payload["error"].(string)
				code, _ := payload["code"].(string)
				// The wire reports both a coarse `code` (legacy alias)
				// and a canonical `errorClass` triage category; prefer
				// `errorClass` for the run-error Code when present so
				// callers see a stable taxonomy. See
				// `docs/agent-runs-protocol.md` §7.
				errorClass, _ := payload["errorClass"].(string)
				finishReason, _ := payload["finishReason"].(string)
				partialText, _ := payload["partialText"].(string)
				resolvedCode := errorClass
				if resolvedCode == "" {
					resolvedCode = code
				}
				rerr := &RunError{
					RunID:        runID,
					Code:         resolvedCode,
					Message:      msg,
					ErrorClass:   errorClass,
					FinishReason: finishReason,
					PartialText:  partialText,
				}
				if retryable, ok := payload["retryable"].(bool); ok {
					rerr.Retryable = &retryable
				}
				// Cost-attribution triple (MANTYX ≥ 2026-09). Failed
				// runs report the failing model call's usage too — see
				// `docs/agent-runs-protocol.md` §7.1.
				rerr.Tokens = parseRunTokens(payload["tokens"])
				if n, ok := parseRunTurns(payload["turns"]); ok {
					rerr.Turns = n
				}
				rerr.Model = parseRunModel(payload["model"])
				if n, ok := parseRunTurns(payload["apiStatus"]); ok {
					rerr.APIStatus = n
				}
				rerr.APICode, _ = payload["apiCode"].(string)
				rerr.StructuredOutput = parseStructuredOutputInfo(payload["structuredOutput"])
				terminalErr = rerr
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
		out, files, err := tool.invoke(ctx, rawArgs)
		if err != nil {
			_ = c.PostToolResult(ctx, runID, toolUseID, "", err.Error())
			return
		}
		_ = c.PostToolResultWithFiles(ctx, runID, toolUseID, out, files)
	}
}

const toolResultPostMaxAttempts = 6

// toolResultPostSleep is the backoff wait between tool-result POST retries.
// Tests may replace it to avoid real delays.
var toolResultPostSleep = time.Sleep

func toolResultPostBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond * time.Duration(1<<attempt)
	if d > 8*time.Second {
		return 8 * time.Second
	}
	return d
}

func isToolResultPostRetryable(err error) bool {
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return true
	}
	var mxErr *Error
	if errors.As(err, &mxErr) {
		if mxErr.HTTPStatus == 429 || mxErr.HTTPStatus >= 500 {
			return true
		}
	}
	return false
}

// PostToolResult sends the SDK's response for a `local_tool_call` event back to
// the server. Either `result` (success) or `errMsg` (failure) should be set.
func (c *Client) PostToolResult(ctx context.Context, runID, toolUseID, result, errMsg string) error {
	return c.postToolResult(ctx, runID, toolUseID, result, errMsg, nil)
}

// PostToolResultWithFiles is like PostToolResult but also attaches files the
// client-resolved tool produced. The bytes are surfaced to the model on the
// next turn as native file parts (see ToolResultFile). Files are only honored
// alongside a successful result; pass them with an empty errMsg.
func (c *Client) PostToolResultWithFiles(ctx context.Context, runID, toolUseID, result string, files []ToolResultFile) error {
	return c.postToolResult(ctx, runID, toolUseID, result, "", files)
}

func (c *Client) postToolResult(ctx context.Context, runID, toolUseID, result, errMsg string, files []ToolResultFile) error {
	body := map[string]any{"toolUseId": toolUseID}
	if errMsg != "" {
		body["error"] = errMsg
	} else {
		// `files` are only honored alongside a `result`; when files are
		// present we always include `result` (even empty) so the server
		// treats the post as a success with attachments rather than a no-op.
		if result != "" || len(files) > 0 {
			body["result"] = result
		}
		if len(files) > 0 {
			body["files"] = files
		}
	}
	path := fmt.Sprintf("/agent-runs/%s/tool-results", pathEscape(runID))
	for attempt := 0; attempt < toolResultPostMaxAttempts; attempt++ {
		err := c.do(ctx, "POST", path, body, nil)
		if err == nil {
			return nil
		}
		if attempt == toolResultPostMaxAttempts-1 || !isToolResultPostRetryable(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		toolResultPostSleep(toolResultPostBackoff(attempt))
	}
	return nil
}

// CancelRun aborts a run server-side. The run row's status moves to
// "cancelled" and a `cancelled` event is appended to its event log.
func (c *Client) CancelRun(ctx context.Context, runID string) error {
	path := fmt.Sprintf("/agent-runs/%s/cancel", pathEscape(runID))
	return c.do(ctx, "POST", path, nil, nil)
}

// RunFeedbackVerdict is thumbs up/down on an agent run.
type RunFeedbackVerdict string

const (
	RunFeedbackUp   RunFeedbackVerdict = "UP"
	RunFeedbackDown RunFeedbackVerdict = "DOWN"
)

// RunFeedbackInput is the body for POST /agent-runs/:runId/feedback.
// See `docs/agent-runs-protocol.md` §9a.
type RunFeedbackInput struct {
	Verdict         RunFeedbackVerdict `json:"verdict"`
	Explanation     string             `json:"explanation,omitempty"`
	ContentSnapshot string             `json:"contentSnapshot,omitempty"`
}

// RunFeedbackResult is the response from POST /agent-runs/:runId/feedback.
type RunFeedbackResult struct {
	ID         string `json:"id"`
	Verdict    string `json:"verdict"`
	TargetKind string `json:"targetKind"`
	AgentRunID string `json:"agentRunId"`
}

const runFeedbackExplanationMax = 8000

// SubmitRunFeedback records thumbs up/down feedback on a run. Requires the
// `feedback:write` OAuth scope (workspace API keys have implicit access).
// Idempotent per run — the first call returns HTTP 201, updates return 200.
func (c *Client) SubmitRunFeedback(ctx context.Context, runID string, input RunFeedbackInput) (*RunFeedbackResult, error) {
	if input.Verdict != RunFeedbackUp && input.Verdict != RunFeedbackDown {
		return nil, &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf(`feedback verdict must be "UP" or "DOWN", got %q`, input.Verdict),
		}
	}
	if len(input.Explanation) > runFeedbackExplanationMax {
		return nil, &Error{
			Code:    "invalid_request",
			Message: fmt.Sprintf("feedback explanation must be <= %d characters", runFeedbackExplanationMax),
		}
	}
	path := fmt.Sprintf("/agent-runs/%s/feedback", pathEscape(runID))
	var out RunFeedbackResult
	if err := c.do(ctx, "POST", path, input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- Evals ----------------------------------------------------------------

// EvalRunStatus is a terminal or in-flight eval run status.
type EvalRunStatus string

const (
	EvalRunQueued    EvalRunStatus = "queued"
	EvalRunRunning   EvalRunStatus = "running"
	EvalRunSucceeded EvalRunStatus = "succeeded"
	EvalRunFailed    EvalRunStatus = "failed"
	EvalRunCancelled EvalRunStatus = "cancelled"
)

var evalTerminalStatuses = map[EvalRunStatus]struct{}{
	EvalRunSucceeded: {},
	EvalRunFailed:    {},
	EvalRunCancelled: {},
}

// InlineEvalCaseSpec is one case in an inline eval dataset.
type InlineEvalCaseSpec struct {
	Name      string           `json:"name,omitempty"`
	Input     map[string]any   `json:"input"`
	Scorers   []map[string]any `json:"scorers,omitempty"`
	ToolMocks map[string]any   `json:"toolMocks,omitempty"`
	Tags      []string         `json:"tags,omitempty"`
}

// InlineEvalDatasetSpec is an inline dataset blob for POST /eval-runs.
type InlineEvalDatasetSpec struct {
	Name      string               `json:"name,omitempty"`
	ToolMocks map[string]any       `json:"toolMocks,omitempty"`
	Cases     []InlineEvalCaseSpec `json:"cases"`
}

// AgentEvalOverrides are saved-agent eval overrides (ignored for inline agent).
type AgentEvalOverrides struct {
	SystemPrompt       string   `json:"systemPrompt,omitempty"`
	SystemPromptAppend string   `json:"systemPromptAppend,omitempty"`
	Model              string   `json:"model,omitempty"`
	LLMProviderID      string   `json:"llmProviderId,omitempty"`
	ReasoningLevel     string   `json:"reasoningLevel,omitempty"`
	DisableTools       *bool    `json:"disableTools,omitempty"`
	ToolAllowlist      []string `json:"toolAllowlist,omitempty"`
	DisabledMocks      []string `json:"disabledMocks,omitempty"`
}

// CreateEvalRunRequest is the body for POST /eval-runs.
// Exactly one of DatasetID/Dataset and AgentID/Agent must be set.
type CreateEvalRunRequest struct {
	DatasetID string                 `json:"datasetId,omitempty"`
	Dataset   *InlineEvalDatasetSpec `json:"dataset,omitempty"`
	AgentID   string                 `json:"agentId,omitempty"`
	Agent     *RunSpec               `json:"-"`
	ModelID   string                 `json:"modelId,omitempty"`
	Overrides *AgentEvalOverrides    `json:"overrides,omitempty"`
	// Tools are client-resolved tool refs merged onto a saved AgentID eval run.
	Tools []ToolRef `json:"-"`
}

// EvalDatasetSummary is a row from GET /eval-datasets.
type EvalDatasetSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CaseCount   int    `json:"caseCount"`
	RunCount    int    `json:"runCount"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// EvalDatasetList is the response from ListEvalDatasets.
type EvalDatasetList struct {
	Datasets []EvalDatasetSummary `json:"datasets"`
}

// EvalCase is one case in a saved eval dataset.
type EvalCase struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Input     map[string]any   `json:"input"`
	Scorers   []map[string]any `json:"scorers"`
	Tags      []string         `json:"tags"`
	ToolMocks map[string]any   `json:"toolMocks"`
	CreatedAt string           `json:"createdAt"`
	UpdatedAt string           `json:"updatedAt"`
}

// EvalDatasetDetail is the response from GetEvalDataset.
type EvalDatasetDetail struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ToolMocks   map[string]any `json:"toolMocks"`
	Cases       []EvalCase     `json:"cases"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
}

// EvalRunAccepted is returned by CreateEvalRun.
type EvalRunAccepted struct {
	RunID     string `json:"runId"`
	Status    string `json:"status"`
	StreamURL string `json:"streamUrl"`
}

// EvalRunSummary is a row from ListEvalRuns.
type EvalRunSummary struct {
	ID             string         `json:"id"`
	DatasetID      string         `json:"datasetId"`
	DatasetName    string         `json:"datasetName"`
	AgentID        string         `json:"agentId"`
	InlineAgent    bool           `json:"inlineAgent"`
	Status         EvalRunStatus  `json:"status"`
	TotalCases     int            `json:"totalCases"`
	CompletedCases int            `json:"completedCases"`
	PassedCases    int            `json:"passedCases"`
	Score          *float64       `json:"score"`
	TokenUsage     map[string]any `json:"tokenUsage"`
	Error          string         `json:"error"`
	AgentOverrides map[string]any `json:"agentOverrides"`
	StartedAt      string         `json:"startedAt"`
	FinishedAt     string         `json:"finishedAt"`
	CreatedAt      string         `json:"createdAt"`
}

// EvalCaseResult is one per-case result on an eval run.
type EvalCaseResult struct {
	ID        string           `json:"id"`
	CaseID    string           `json:"caseId"`
	Case      EvalCase         `json:"case"`
	FinalText string           `json:"finalText"`
	ToolCalls []map[string]any `json:"toolCalls"`
	Scores    []map[string]any `json:"scores"`
	Passed    bool             `json:"passed"`
	Score     float64          `json:"score"`
	Tokens    map[string]any   `json:"tokens"`
	LatencyMs int              `json:"latencyMs"`
	Error     string           `json:"error"`
	CreatedAt string           `json:"createdAt"`
}

// EvalRunDetail is the response from GetEvalRun.
type EvalRunDetail struct {
	EvalRunSummary
	InlineAgentSpec   map[string]any   `json:"inlineAgentSpec"`
	AgentSpecSnapshot map[string]any   `json:"agentSpecSnapshot"`
	UpdatedAt         string           `json:"updatedAt"`
	Results           []EvalCaseResult `json:"results"`
}

// EvalRunList is the response from ListEvalRuns.
type EvalRunList struct {
	Runs   []EvalRunSummary `json:"runs"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// EvalRunCompareSide is one side of GET /eval-runs/compare.
type EvalRunCompareSide struct {
	ID                string         `json:"id"`
	AgentID           string         `json:"agentId"`
	InlineAgent       bool           `json:"inlineAgent"`
	Status            string         `json:"status"`
	TotalCases        int            `json:"totalCases"`
	CompletedCases    int            `json:"completedCases"`
	PassedCases       int            `json:"passedCases"`
	Score             *float64       `json:"score"`
	TokenUsage        map[string]any `json:"tokenUsage"`
	AgentOverrides    map[string]any `json:"agentOverrides"`
	AgentSpecSnapshot map[string]any `json:"agentSpecSnapshot"`
	StartedAt         string         `json:"startedAt"`
	FinishedAt        string         `json:"finishedAt"`
	CreatedAt         string         `json:"createdAt"`
}

// EvalRunCompareCase is one aligned case in a compare response.
type EvalRunCompareCase struct {
	CaseID    string         `json:"caseId"`
	CaseName  string         `json:"caseName"`
	CaseInput map[string]any `json:"caseInput"`
	A         map[string]any `json:"a"`
	B         map[string]any `json:"b"`
}

// EvalRunCompare is the response from CompareEvalRuns.
type EvalRunCompare struct {
	DatasetID   string               `json:"datasetId"`
	DatasetName string               `json:"datasetName"`
	RunA        EvalRunCompareSide   `json:"runA"`
	RunB        EvalRunCompareSide   `json:"runB"`
	Cases       []EvalRunCompareCase `json:"cases"`
}

// EvalRunEvent is one SSE frame from StreamEvalRun.
type EvalRunEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"-"`
}

// ListEvalRunsOptions filters GET /eval-runs.
type ListEvalRunsOptions struct {
	DatasetID string
	AgentID   string
	Status    string
	Limit     int
	Offset    int
}

// RunEvalOptions configures the blocking RunEval helper.
type RunEvalOptions struct {
	Tools        []ToolRef
	OnEvent      func(EvalRunEvent)
	PollInterval time.Duration
}

// ListEvalDatasets returns saved eval datasets in the workspace.
func (c *Client) ListEvalDatasets(ctx context.Context) (EvalDatasetList, error) {
	var out EvalDatasetList
	err := c.do(ctx, "GET", "/eval-datasets", nil, &out)
	return out, err
}

// GetEvalDataset returns one eval dataset with cases.
func (c *Client) GetEvalDataset(ctx context.Context, datasetID string) (EvalDatasetDetail, error) {
	var out EvalDatasetDetail
	path := fmt.Sprintf("/eval-datasets/%s", pathEscape(datasetID))
	err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}

// CreateEvalRun queues an eval run.
func (c *Client) CreateEvalRun(ctx context.Context, req CreateEvalRunRequest) (EvalRunAccepted, error) {
	if err := validateCreateEvalRunRequest(req); err != nil {
		return EvalRunAccepted{}, err
	}
	if err := resolveLocalRefs(ctx, req.Tools, c.httpClient); err != nil {
		return EvalRunAccepted{}, err
	}
	var out EvalRunAccepted
	err := c.do(ctx, "POST", "/eval-runs", serializeCreateEvalRunRequest(req), &out)
	return out, err
}

// ListEvalRuns lists eval runs with optional filters.
func (c *Client) ListEvalRuns(ctx context.Context, opts ListEvalRunsOptions) (EvalRunList, error) {
	q := url.Values{}
	if opts.DatasetID != "" {
		q.Set("datasetId", opts.DatasetID)
	}
	if opts.AgentID != "" {
		q.Set("agentId", opts.AgentID)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	path := "/eval-runs"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out EvalRunList
	err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}

// CompareEvalRuns aligns two eval runs case-by-case.
func (c *Client) CompareEvalRuns(ctx context.Context, runA, runB string) (EvalRunCompare, error) {
	path := fmt.Sprintf("/eval-runs/compare?a=%s&b=%s", url.QueryEscape(runA), url.QueryEscape(runB))
	var out EvalRunCompare
	err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}

// GetEvalRun returns an eval run with per-case results.
func (c *Client) GetEvalRun(ctx context.Context, runID string) (EvalRunDetail, error) {
	var out EvalRunDetail
	path := fmt.Sprintf("/eval-runs/%s", pathEscape(runID))
	err := c.do(ctx, "GET", path, nil, &out)
	return out, err
}

// StreamEvalRun tails eval run progress over SSE until a terminal event.
func (c *Client) StreamEvalRun(ctx context.Context, runID string) (<-chan EvalRunEvent, <-chan error) {
	events := make(chan EvalRunEvent, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		path := fmt.Sprintf("/eval-runs/%s/stream", pathEscape(runID))
		resp, err := c.openSSEStream(ctx, path, 0)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- c.errorFromResponse(resp)
			return
		}
		err = readSseStream(resp.Body, func(ev SseEvent) bool {
			if ev.Data == "" {
				return true
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(ev.Data), &raw); err != nil {
				errs <- &Error{Code: "parse_error", Message: "invalid eval SSE JSON: " + err.Error()}
				return false
			}
			typeVal, _ := raw["type"].(string)
			if typeVal == "" {
				typeVal = "message"
			}
			data := make(map[string]any, len(raw))
			for k, v := range raw {
				if k != "type" {
					data[k] = v
				}
			}
			evt := EvalRunEvent{Type: typeVal, Data: data}
			select {
			case events <- evt:
			case <-ctx.Done():
				return false
			}
			if typeVal == "run_completed" || typeVal == "run_error" || typeVal == "run_cancelled" {
				return false
			}
			return true
		})
		if err != nil {
			errs <- err
		}
	}()
	return events, errs
}

// CancelEvalRun cancels a queued or running eval run.
func (c *Client) CancelEvalRun(ctx context.Context, runID string) error {
	path := fmt.Sprintf("/eval-runs/%s/cancel", pathEscape(runID))
	var out struct {
		OK bool `json:"ok"`
	}
	return c.do(ctx, "POST", path, nil, &out)
}

// RunEval starts an eval run and blocks until it reaches a terminal status.
func (c *Client) RunEval(ctx context.Context, req CreateEvalRunRequest, opts RunEvalOptions) (EvalRunDetail, error) {
	tools := opts.Tools
	if len(tools) == 0 {
		tools = req.Tools
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	if err := resolveLocalRefs(ctx, tools, c.httpClient); err != nil {
		return EvalRunDetail{}, err
	}
	defer closeMcpRefs(tools)
	handlers := collectLocalHandlers(tools)

	accepted, err := c.CreateEvalRun(ctx, req)
	if err != nil {
		return EvalRunDetail{}, err
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	var streamDone chan struct{}
	if opts.OnEvent != nil || len(tools) > 0 {
		events, errs := c.StreamEvalRun(ctx, accepted.RunID)
		streamDone = make(chan struct{})
		go func() {
			defer close(streamDone)
			for ev := range events {
				if ev.Type == "local_tool_call" && len(tools) > 0 {
					agentRunID, _ := ev.Data["agentRunId"].(string)
					if agentRunID == "" {
						if top, ok := ev.Data["data"].(map[string]any); ok {
							agentRunID, _ = top["agentRunId"].(string)
						}
					}
					if agentRunID != "" {
						runEv := evalEventToRunEvent(ev)
						c.dispatchLocalTool(ctx, agentRunID, runEv, handlers)
					}
				}
				if opts.OnEvent != nil {
					opts.OnEvent(ev)
				}
			}
			<-errs
		}()
	}
	for {
		if err := ctx.Err(); err != nil {
			return EvalRunDetail{}, &NetworkError{Inner: &Error{Message: "eval run wait aborted", Code: "network"}, Cause: err}
		}
		detail, err := c.GetEvalRun(ctx, accepted.RunID)
		if err != nil {
			return EvalRunDetail{}, err
		}
		if _, ok := evalTerminalStatuses[detail.Status]; ok {
			if streamDone != nil {
				<-streamDone
			}
			return detail, nil
		}
		select {
		case <-ctx.Done():
			return EvalRunDetail{}, &NetworkError{Inner: &Error{Message: "eval run wait aborted", Code: "network"}, Cause: ctx.Err()}
		case <-time.After(poll):
		}
	}
}

func validateCreateEvalRunRequest(req CreateEvalRunRequest) error {
	hasDataset := req.DatasetID != "" || req.Dataset != nil
	hasAgent := req.AgentID != "" || req.Agent != nil
	if !hasDataset {
		return &Error{Code: "invalid_request", Message: "CreateEvalRun requires exactly one of DatasetID or Dataset"}
	}
	if req.DatasetID != "" && req.Dataset != nil {
		return &Error{Code: "invalid_request", Message: "CreateEvalRun accepts only one of DatasetID or Dataset"}
	}
	if !hasAgent {
		return &Error{Code: "invalid_request", Message: "CreateEvalRun requires exactly one of AgentID or Agent"}
	}
	if req.AgentID != "" && req.Agent != nil {
		return &Error{Code: "invalid_request", Message: "CreateEvalRun accepts only one of AgentID or Agent"}
	}
	return nil
}

func serializeCreateEvalRunRequest(req CreateEvalRunRequest) map[string]any {
	body := map[string]any{}
	if req.DatasetID != "" {
		body["datasetId"] = req.DatasetID
	}
	if req.Dataset != nil {
		body["dataset"] = req.Dataset
	}
	if req.AgentID != "" {
		body["agentId"] = req.AgentID
	}
	if req.Agent != nil {
		body["agent"] = serializeEvalAgentSpec(*req.Agent)
	}
	if len(req.Tools) > 0 && req.AgentID != "" {
		body["tools"] = toolWire(req.Tools)
	}
	if req.ModelID != "" {
		body["modelId"] = req.ModelID
	}
	if req.Overrides != nil {
		body["overrides"] = req.Overrides
	}
	return body
}

func serializeEvalAgentSpec(spec RunSpec) map[string]any {
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
	if spec.OutputSchema != nil {
		body["outputSchema"] = spec.OutputSchema
	}
	if spec.LoopDetection != nil {
		body["loopDetection"] = spec.LoopDetection
	}
	if spec.ToolBudgets != nil {
		body["toolBudgets"] = serializeToolBudgets(spec.ToolBudgets)
	}
	if spec.Supervisor != nil {
		body["supervisor"] = spec.Supervisor
	}
	if spec.Plan != nil {
		body["plan"] = spec.Plan
	}
	if len(spec.Metadata) > 0 {
		body["metadata"] = spec.Metadata
	}
	return body
}

func evalEventToRunEvent(ev EvalRunEvent) RunEvent {
	data := make(map[string]any, len(ev.Data))
	for k, v := range ev.Data {
		data[k] = v
	}
	return RunEvent{Type: ev.Type, Data: data}
}

// ----- HTTP plumbing --------------------------------------------------------

// openSSEStream opens the SSE stream against `path` with at-most-one
// refresh + retry on 401 when a TokenSource is configured. The caller
// is responsible for consuming the response body and reconnecting on
// mid-stream disconnects.
func (c *Client) openSSEStream(ctx context.Context, path string, lastSeq int) (*http.Response, error) {
	openOnce := func(reason TokenRequestReason) (*http.Response, error) {
		req, err := c.newRequest(ctx, "GET", path, nil, reason)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		if lastSeq > 0 {
			req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", lastSeq))
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
		}
		return resp, nil
	}
	resp, err := openOnce(ReasonInitial)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.tokenSource != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return openOnce(ReasonUnauthorized)
	}
	return resp, nil
}

// resolveBearer returns the bearer credential for the next request.
// Static APIKey / AccessToken clients reach into the cached value;
// TokenSource clients delegate so the source can refresh expired access
// tokens before we hit the wire. Pass ReasonUnauthorized immediately
// after a 401 to force a refresh.
func (c *Client) resolveBearer(ctx context.Context, reason TokenRequestReason) (string, error) {
	if c.tokenSource != nil {
		return c.tokenSource.Token(ctx, reason)
	}
	return c.apiKey, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any, reason TokenRequestReason) (*http.Request, error) {
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
	bearer, err := c.resolveBearer(ctx, reason)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doWithRetry(ctx, method, path, body, out, ReasonInitial)
}

// doWithRetry runs one HTTP attempt and, on 401 with a configured
// TokenSource, refreshes and retries the request exactly once. Static-
// credential clients fall straight through to *AuthError.
func (c *Client) doWithRetry(ctx context.Context, method, path string, body any, out any, reason TokenRequestReason) error {
	req, err := c.newRequest(ctx, method, path, body, reason)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
	}
	if resp.StatusCode == http.StatusUnauthorized && c.tokenSource != nil && reason == ReasonInitial {
		// Drain + close so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return c.doWithRetry(ctx, method, path, body, out, ReasonUnauthorized)
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
	// `Required` is the verbatim `required` field on `403 insufficient_scope`
	// responses. The server returns either a single scope (string) or, on
	// multi-scope routes, an array — we accept json.RawMessage so we can
	// parse either shape downstream. See `docs/agent-runs-protocol.md` §2.3.
	body := struct {
		Error    string          `json:"error"`
		Code     string          `json:"code"`
		Hint     string          `json:"hint"`
		Required json.RawMessage `json:"required"`
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
	// `403 insufficient_scope` is the OAuth "missing scope" signal.
	if resp.StatusCode == http.StatusForbidden &&
		(body.Error == "insufficient_scope" || body.Code == "insufficient_scope") {
		required := parseRequiredScopes(body.Required, resp.Header.Get("WWW-Authenticate"))
		scopeMsg := msg
		if scopeMsg == "" || scopeMsg == "insufficient_scope" {
			if len(required) > 0 {
				plural := ""
				if len(required) > 1 {
					plural = "s"
				}
				scopeMsg = fmt.Sprintf("Missing OAuth scope%s: %s", plural, strings.Join(required, ", "))
			} else {
				scopeMsg = "OAuth access token is missing a required scope"
			}
		}
		base.Message = scopeMsg
		base.Code = "insufficient_scope"
		return &ScopeError{Inner: base, RequiredScopes: required}
	}
	if base.Code == "" {
		base.Code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return base
}

// parseRequiredScopes extracts the list of scopes the server reported
// as required for a route, from either the response body's `required`
// field (string or []string) or the `WWW-Authenticate: Bearer
// error="insufficient_scope", scope="…"` header (RFC 6750).
func parseRequiredScopes(raw json.RawMessage, wwwAuthenticate string) []string {
	if len(raw) > 0 {
		var arr []string
		if err := json.Unmarshal(raw, &arr); err == nil {
			out := arr[:0]
			for _, s := range arr {
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		var one string
		if err := json.Unmarshal(raw, &one); err == nil && one != "" {
			return []string{one}
		}
	}
	if wwwAuthenticate != "" {
		// Crude but spec-compliant: look for scope="..." inside the header.
		const marker = `scope="`
		if i := strings.Index(wwwAuthenticate, marker); i >= 0 {
			rest := wwwAuthenticate[i+len(marker):]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				parts := strings.Fields(rest[:j])
				if len(parts) > 0 {
					return parts
				}
			}
		}
	}
	return nil
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
	if spec.OutputSchema != nil {
		body["outputSchema"] = spec.OutputSchema
	}
	if spec.LoopDetection != nil {
		body["loopDetection"] = spec.LoopDetection
	}
	if spec.ToolBudgets != nil {
		body["toolBudgets"] = serializeToolBudgets(spec.ToolBudgets)
	}
	if spec.Supervisor != nil {
		body["supervisor"] = spec.Supervisor
	}
	if spec.Plan != nil {
		body["plan"] = spec.Plan
	}
	for k, v := range serializeTurnInput(spec.Prompt, spec.Messages, spec.Attachments) {
		body[k] = v
	}
	if len(spec.Metadata) > 0 {
		body["metadata"] = spec.Metadata
	}
	return body
}

func agentIdentityPresent(agentID, systemPrompt string, messages []Message, attachments []map[string]any, prompt string) bool {
	if agentID != "" {
		return true
	}
	if systemPrompt != "" {
		return true
	}
	identityMessages := messages
	if len(identityMessages) == 0 && len(attachments) > 0 && prompt != "" {
		identityMessages = []Message{{Role: "user", Content: prompt}}
	}
	for _, m := range identityMessages {
		if m.Role == "system" && strings.TrimSpace(m.Content) != "" {
			return true
		}
	}
	return false
}

func serializeTurnInput(prompt string, messages []Message, attachments []map[string]any) map[string]any {
	if len(messages) > 0 {
		return map[string]any{"messages": messages}
	}
	if prompt != "" {
		if len(attachments) > 0 {
			return map[string]any{
				"messages": []Message{{
					Role:        "user",
					Content:     prompt,
					Attachments: attachments,
				}},
			}
		}
		return map[string]any{"prompt": prompt}
	}
	return map[string]any{}
}

// serializeToolBudgets returns a wire-shaped representation of a
// ToolBudgets map. nil → nil; an empty map is preserved as `{}` (the
// "clear runtime defaults" sentinel).
func serializeToolBudgets(b ToolBudgets) map[string]any {
	if b == nil {
		return nil
	}
	out := make(map[string]any, len(b))
	for name, entry := range b {
		out[name] = map[string]any{"maxCalls": entry.MaxCalls}
	}
	return out
}

// ParseRunOutput JSON-decodes the terminal text of a RunResult into `dest`.
//
// Provider-enforced output should parse as JSON, but transient model errors
// (refusal text, truncation under
// max_tokens pressure, exotic Unicode) can still produce strings that
// fail to json.Unmarshal in rare edge cases — this helper centralises
// that brittle step and surfaces a typed *ParseError on failure with the
// original text preserved on err.Text.
//
// `dest` should be a pointer to whatever struct / map you want the JSON
// reply decoded into:
//
//	var report struct {
//		City         string  `json:"city"`
//		TemperatureC float64 `json:"temperature_c"`
//	}
//	if err := mantyx.ParseRunOutput(result, &report); err != nil { ... }
func ParseRunOutput(result RunResult, dest any) error {
	if err := json.Unmarshal([]byte(result.Text), dest); err != nil {
		return &ParseError{RunID: result.RunID, Text: result.Text, Cause: err}
	}
	return nil
}

// parseRunTokens defensively decodes a wire `tokens` object into a
// *RunTokenUsage. Returns nil when the value is missing or not an
// object — that's the "no usage data" sentinel against legacy MANTYX
// servers. Unknown / missing buckets default to 0 (the protocol
// contract is that misbehaving engines clamp to non-negative integers;
// the SDK mirrors that here so dashboards never see negatives).
func parseRunTokens(raw any) *RunTokenUsage {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return &RunTokenUsage{
		InputTokens:     toNonNegativeInt(m["inputTokens"]),
		CachedTokens:    toNonNegativeInt(m["cachedTokens"]),
		ReasoningTokens: toNonNegativeInt(m["reasoningTokens"]),
		OutputTokens:    toNonNegativeInt(m["outputTokens"]),
	}
}

// parseRunTurns coerces a wire `turns` value into a non-negative int.
// Returns (0, false) when the value is missing or unparseable so the
// caller can leave `RunResult.Turns` at zero against legacy servers.
func parseRunTurns(raw any) (int, bool) {
	f, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	if f < 0 {
		return 0, true
	}
	return int(f), true
}

// parseStructuredOutputInfo decodes optional terminal output-schema
// observability metadata. Invalid or partial values are ignored.
func parseStructuredOutputInfo(raw any) *StructuredOutputInfo {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	schemaRequested, okRequested := m["schemaRequested"].(bool)
	schemaEnforced, okEnforced := m["schemaEnforced"].(bool)
	mechanism, okMechanism := m["enforcementMechanism"].(string)
	fallback, okFallback := m["unconstrainedFallbackOccurred"].(bool)
	if !okRequested || !okEnforced || !okMechanism || !okFallback {
		return nil
	}
	if mechanism != string(StructuredOutputEnforcementNativeSchema) &&
		mechanism != string(StructuredOutputEnforcementSyntheticTool) &&
		mechanism != string(StructuredOutputEnforcementNone) {
		return nil
	}
	return &StructuredOutputInfo{
		SchemaRequested:               schemaRequested,
		SchemaEnforced:                schemaEnforced,
		EnforcementMechanism:          StructuredOutputEnforcementMechanism(mechanism),
		UnconstrainedFallbackOccurred: fallback,
	}
}

// parseRunModel decodes a wire `model` object into a *RunModelInfo.
// Returns nil when the value is missing or not an object — the "no
// usage data" sentinel for legacy servers.
func parseRunModel(raw any) *RunModelInfo {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := &RunModelInfo{}
	if s, ok := m["id"].(string); ok {
		out.ID = s
	}
	if s, ok := m["provider"].(string); ok {
		out.Provider = s
	}
	if s, ok := m["vendorModelId"].(string); ok {
		out.VendorModelID = s
	}
	if s, ok := m["reasoningEffort"].(string); ok {
		out.ReasoningEffort = s
	}
	return out
}

// parseTaskPlan decodes a wire `plan` object from a terminal result or v2
// `task_plan` event. Prefers the canonical nested `plan` field when present.
func parseTaskPlan(raw any) *TaskPlan {
	if m, ok := raw.(map[string]any); ok {
		if nested, ok := m["plan"].(map[string]any); ok {
			if p := parseTaskPlanSnapshot(nested); p != nil {
				return p
			}
		}
		return parseTaskPlanSnapshot(m)
	}
	return nil
}

func parseTaskPlanSnapshot(raw map[string]any) *TaskPlan {
	stepsRaw, ok := raw["steps"].([]any)
	if !ok {
		return nil
	}
	steps := make([]TaskPlanStep, 0, len(stepsRaw))
	for _, entry := range stepsRaw {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		title, _ := row["title"].(string)
		status, _ := row["status"].(string)
		if title == "" || status == "" {
			continue
		}
		if status != string(TaskPlanPending) &&
			status != string(TaskPlanInProgress) &&
			status != string(TaskPlanDone) &&
			status != string(TaskPlanBlocked) &&
			status != string(TaskPlanSkipped) {
			continue
		}
		step := TaskPlanStep{Title: title, Status: TaskPlanStepStatus(status)}
		if id, ok := row["id"].(string); ok && id != "" {
			step.ID = id
		}
		steps = append(steps, step)
	}
	out := &TaskPlan{Steps: steps}
	if v, ok := raw["v"].(float64); ok {
		out.V = int(v)
	}
	if planID, ok := raw["planId"].(string); ok && planID != "" {
		out.PlanID = planID
	}
	if revision, ok := raw["revision"].(float64); ok {
		out.Revision = int(revision)
	}
	if mode, ok := raw["mode"].(string); ok && mode != "" {
		out.Mode = mode
	}
	if source, ok := raw["source"].(string); ok && source != "" {
		out.Source = source
	}
	if brief, ok := raw["brief"].(string); ok && brief != "" {
		out.Brief = brief
	}
	return out
}

// TaskPlanFromEventData parses a `task_plan` event payload, preferring the
// canonical v2 `plan` snapshot.
func TaskPlanFromEventData(data map[string]any) *TaskPlan {
	if nested, ok := data["plan"].(map[string]any); ok {
		if p := parseTaskPlanSnapshot(nested); p != nil {
			return p
		}
	}
	return parseTaskPlanSnapshot(data)
}

func runEventPayload(data map[string]any) map[string]any {
	if nested, ok := data["data"].(map[string]any); ok {
		return nested
	}
	return data
}

func toNonNegativeInt(raw any) int {
	f, ok := raw.(float64)
	if !ok {
		return 0
	}
	if f < 0 {
		return 0
	}
	return int(f)
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
