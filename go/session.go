package mantyx

import (
	"context"
	"fmt"
)

// Session is a multi-turn conversation handle. The server owns the message
// history; the SDK holds the local-tool handlers in memory.
type Session struct {
	ID        string
	client    *Client
	handlers  *localToolRegistry
	toolsWire []map[string]any // optional refresh of tool defs sent on each Send.
	tools     []ToolRef        // retained so End() can close MCP transports.
}

// SendOption configures a single Send call.
type SendOption func(*sendOptions)

type sendOptions struct {
	OnAssistantDelta func(string)
	OnEvent          func(RunEvent)
	Metadata         map[string]string
	ReasoningLevel   *ReasoningLevel
	OutputSchema     *OutputSchema
	LoopDetection    *LoopDetection
	ToolBudgets      ToolBudgets
	Supervisor       *Supervisor
	Plan             *Plan
	Messages         []Message
	Attachments      []map[string]any
}

// WithAssistantDelta registers a callback that receives streaming assistant text.
func WithAssistantDelta(cb func(string)) SendOption {
	return func(o *sendOptions) { o.OnAssistantDelta = cb }
}

// WithEventCallback registers a callback that receives every run event.
func WithEventCallback(cb func(RunEvent)) SendOption {
	return func(o *sendOptions) { o.OnEvent = cb }
}

// WithMetadata attaches per-message metadata that the server merges on top of
// the session's metadata at run-creation time (run-level keys win). Useful for
// tagging individual turns (e.g. trace_id) while keeping shared tags on the
// session itself. See RunSpec.Metadata for the validation rules.
func WithMetadata(meta map[string]string) SendOption {
	return func(o *sendOptions) { o.Metadata = meta }
}

// WithReasoningLevel overrides the session's stored ReasoningLevel for this
// single run. Build the value with ReasoningOff/Low/Medium/High or
// ReasoningEffort(n).
func WithReasoningLevel(level *ReasoningLevel) SendOption {
	return func(o *sendOptions) { o.ReasoningLevel = level }
}

// WithOutputSchema overrides the session's stored OutputSchema for this
// single run. Pass `&mantyx.OutputSchema{Schema: ...}` to attach a JSON
// Schema to the assistant's reply for this turn only.
func WithOutputSchema(schema *OutputSchema) SendOption {
	return func(o *sendOptions) { o.OutputSchema = schema }
}

// WithLoopDetection overrides the session's stored LoopDetection for this
// single run. Build the value with LoopDetectionThresholds(...) or pass
// LoopDetectionDisabled() to opt this turn out of the guard. The override
// applies to that one run only and does not mutate the session's stored
// value. See `docs/agent-runs-protocol.md` §4.6.
func WithLoopDetection(ld *LoopDetection) SendOption {
	return func(o *sendOptions) { o.LoopDetection = ld }
}

// WithToolBudgets overrides the session's stored ToolBudgets for this
// single run. Pass an empty (non-nil) ToolBudgets map (e.g.
// `mantyx.ToolBudgets{}`) to clear the runtime defaults; the override
// applies to that one run only and does not mutate the session's stored
// value. See `docs/agent-runs-protocol.md` §4.7.
func WithToolBudgets(b ToolBudgets) SendOption {
	return func(o *sendOptions) { o.ToolBudgets = b }
}

// WithSupervisor overrides the session's stored Supervisor for this single
// run. Build the value with SupervisorInterval(...) or pass
// SupervisorDisabled() to opt this turn out of the platform judge. The
// override applies to that one run only and does not mutate the session's
// stored value. See `docs/agent-runs-protocol.md` §4.8.
func WithSupervisor(s *Supervisor) SendOption {
	return func(o *sendOptions) { o.Supervisor = s }
}

// WithPlan overrides the session's stored Plan for this single run. Build
// the value with PlanAuto, PlanWithSteps, PlanOnly, or PlanDisabled. The
// override applies to that one run only and does not mutate the session's
// stored value. See `docs/agent-runs-protocol.md` §4.9.
func WithPlan(p *Plan) SendOption {
	return func(o *sendOptions) { o.Plan = p }
}

// WithMessages sends a multi-role turn (or turns) instead of a single prompt.
// See docs/agent-runs-protocol.md §4.0.1.
func WithMessages(msgs []Message) SendOption {
	return func(o *sendOptions) { o.Messages = msgs }
}

// WithAttachments attaches file inputs to a single prompt turn. Ignored when
// WithMessages is also set.
func WithAttachments(atts ...map[string]any) SendOption {
	return func(o *sendOptions) { o.Attachments = atts }
}

// Send sends a user turn and waits for the agent's reply.
func (s *Session) Send(ctx context.Context, prompt string, opts ...SendOption) (RunResult, error) {
	o := sendOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if err := o.OutputSchema.validate(); err != nil {
		return RunResult{}, err
	}
	if err := o.LoopDetection.validate(); err != nil {
		return RunResult{}, err
	}
	if err := o.ToolBudgets.validate(); err != nil {
		return RunResult{}, err
	}
	if err := o.Supervisor.validate(); err != nil {
		return RunResult{}, err
	}
	if err := o.Plan.validate(); err != nil {
		return RunResult{}, err
	}
	body := s.buildMessageBody(prompt, o)
	created, err := s.client.createRun(ctx, fmt.Sprintf("/agent-sessions/%s/messages", pathEscape(s.ID)), body)
	if err != nil {
		return RunResult{}, err
	}
	return s.client.driveRunWithRegistry(ctx, created.RunID, s.handlers, o.OnAssistantDelta, o.OnEvent)
}

// RunPlan is sugar for Send with PlanOnly — classify (or accept caller
// steps) and return the structured checklist without executing the agent
// loop.
func (s *Session) RunPlan(ctx context.Context, prompt string, steps []string, brief string, opts ...SendOption) (RunResult, error) {
	p := PlanOnly(steps...)
	if brief != "" {
		p = p.WithBrief(brief)
	}
	opts = append([]SendOption{WithPlan(p)}, opts...)
	return s.Send(ctx, prompt, opts...)
}

// Stream is the streaming variant of Send.
func (s *Session) Stream(ctx context.Context, prompt string, opts ...SendOption) (<-chan RunEvent, error) {
	o := sendOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if err := o.OutputSchema.validate(); err != nil {
		return nil, err
	}
	if err := o.LoopDetection.validate(); err != nil {
		return nil, err
	}
	if err := o.ToolBudgets.validate(); err != nil {
		return nil, err
	}
	if err := o.Supervisor.validate(); err != nil {
		return nil, err
	}
	if err := o.Plan.validate(); err != nil {
		return nil, err
	}
	body := s.buildMessageBody(prompt, o)
	created, err := s.client.createRun(ctx, fmt.Sprintf("/agent-sessions/%s/messages", pathEscape(s.ID)), body)
	if err != nil {
		return nil, err
	}
	ch := make(chan RunEvent, 32)
	go func() {
		defer close(ch)
		_, _ = s.client.consumeStream(ctx, created.RunID, s.handlers, func(ev RunEvent) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		})
	}()
	return ch, nil
}

func (s *Session) buildMessageBody(prompt string, o sendOptions) map[string]any {
	body := serializeTurnInput(prompt, o.Messages, o.Attachments)
	if s.toolsWire != nil {
		body["tools"] = s.toolsWire
	}
	if len(o.Metadata) > 0 {
		body["metadata"] = o.Metadata
	}
	if o.ReasoningLevel != nil {
		body["reasoningLevel"] = o.ReasoningLevel
	}
	if o.OutputSchema != nil {
		body["outputSchema"] = o.OutputSchema
	}
	if o.LoopDetection != nil {
		body["loopDetection"] = o.LoopDetection
	}
	if o.ToolBudgets != nil {
		body["toolBudgets"] = serializeToolBudgets(o.ToolBudgets)
	}
	if o.Supervisor != nil {
		body["supervisor"] = o.Supervisor
	}
	if o.Plan != nil {
		body["plan"] = o.Plan
	}
	return body
}

// History returns the persisted message history for the session.
func (s *Session) History(ctx context.Context) ([]Message, error) {
	info, err := s.client.GetSessionInfo(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	return info.Messages, nil
}

// Info returns a snapshot of the session row.
func (s *Session) Info(ctx context.Context) (SessionInfo, error) {
	return s.client.GetSessionInfo(ctx, s.ID)
}

// Events replays this session's conversation as realtime-style event frames.
func (s *Session) Events(ctx context.Context, opts GetSessionEventsOptions) ([]RunEvent, error) {
	return s.client.GetSessionEvents(ctx, s.ID, opts)
}

// End marks the session terminal and closes any MCP transports the SDK
// opened on the session's behalf.
func (s *Session) End(ctx context.Context) error {
	err := s.client.EndSession(ctx, s.ID)
	if cerr := closeMcpRefs(s.tools); cerr != nil && err == nil {
		err = cerr
	}
	return err
}
