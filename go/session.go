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
	handlers  localToolRegistry
	toolsWire []map[string]any // optional refresh of tool defs sent on each Send.
}

// SendOption configures a single Send call.
type SendOption func(*sendOptions)

type sendOptions struct {
	OnAssistantDelta func(string)
	OnEvent          func(RunEvent)
	Metadata         map[string]string
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

// Send sends a user turn and waits for the agent's reply.
func (s *Session) Send(ctx context.Context, prompt string, opts ...SendOption) (RunResult, error) {
	o := sendOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	body := map[string]any{"prompt": prompt}
	if s.toolsWire != nil {
		body["tools"] = s.toolsWire
	}
	if len(o.Metadata) > 0 {
		body["metadata"] = o.Metadata
	}
	created, err := s.client.createRun(ctx, fmt.Sprintf("/agent-sessions/%s/messages", pathEscape(s.ID)), body)
	if err != nil {
		return RunResult{}, err
	}
	tools := make([]ToolRef, 0, len(s.handlers))
	for _, h := range s.handlers {
		tools = append(tools, h)
	}
	return s.client.driveRun(ctx, created.RunID, tools, o.OnAssistantDelta, o.OnEvent)
}

// Stream is the streaming variant of Send.
func (s *Session) Stream(ctx context.Context, prompt string, opts ...SendOption) (<-chan RunEvent, error) {
	o := sendOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	body := map[string]any{"prompt": prompt}
	if s.toolsWire != nil {
		body["tools"] = s.toolsWire
	}
	if len(o.Metadata) > 0 {
		body["metadata"] = o.Metadata
	}
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

// History returns the persisted message history for the session.
func (s *Session) History(ctx context.Context) ([]Message, error) {
	info, err := s.client.GetSessionInfo(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	return info.Messages, nil
}

// End marks the session terminal.
func (s *Session) End(ctx context.Context) error {
	return s.client.EndSession(ctx, s.ID)
}
