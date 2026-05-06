package a2asrv

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	mantyx "github.com/mantyx-io/mantyx-go-sdk"
)

// Conversation describes how A2A contextIDs map onto MANTYX sessions.
type Conversation int

const (
	// ConversationAuto opens a MANTYX session on the first contact with a
	// new contextID and reuses it on every subsequent call. Multi-turn out
	// of the box.
	ConversationAuto Conversation = iota
	// ConversationStateless reduces every A2A call to an independent
	// runAgent — no conversational memory.
	ConversationStateless
)

// AgentSpec describes the MANTYX agent that should answer A2A requests.
//
// Mirrors mantyx.RunSpec / mantyx.SessionSpec: either AgentID (persisted)
// or SystemPrompt (ephemeral) is required.
type AgentSpec struct {
	// AgentID references a persisted MANTYX agent in this workspace.
	// Mutually exclusive with SystemPrompt.
	AgentID string
	// SystemPrompt for an ephemeral inline agent. Mutually exclusive with
	// AgentID.
	SystemPrompt   string
	ModelID        string
	Name           string
	Tools          []mantyx.ToolRef
	ReasoningLevel *mantyx.ReasoningLevel
	OutputSchema   *mantyx.OutputSchema
	Metadata       map[string]string
	Budgets        map[string]any
}

func (s AgentSpec) validate() error {
	if s.AgentID == "" && s.SystemPrompt == "" {
		return errors.New("a2asrv: AgentSpec requires AgentID or SystemPrompt")
	}
	return nil
}

// ExecutorOptions configures NewExecutor.
type ExecutorOptions struct {
	// Conversation controls the contextID→session mapping. Defaults to
	// ConversationAuto.
	Conversation Conversation
	// MaxSessions caps the LRU map of contextID→Session entries.
	// Defaults to 1024.
	MaxSessions int
	// OnAssistantDelta lets callers customize how token deltas flow into
	// the A2A event stream. The default fan-outs each delta as a
	// TaskStatusUpdateEvent with state=working containing the chunk as a
	// text part — exactly what message/stream clients expect.
	//
	// Implementations should call yield once per event they want to emit.
	OnAssistantDelta func(execCtx *a2asrv.ExecutorContext, delta string, yield func(a2a.Event) bool)
}

// Executor implements a2asrv.AgentExecutor and dispatches every Execute
// call into a MANTYX agent (a one-shot run or a per-contextID session).
type Executor struct {
	client *mantyx.Client
	agent  AgentSpec
	opts   ExecutorOptions

	mu       sync.Mutex
	sessions map[string]*list.Element // contextID -> *list.Element wrapping *sessionEntry
	lru      *list.List
}

type sessionEntry struct {
	contextID string
	session  *mantyx.Session
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor constructs a MANTYX-backed AgentExecutor. The returned value
// can be plugged into a2asrv.NewHandler exactly like any other custom
// executor; see Serve for a one-call helper that wires it into net/http.
func NewExecutor(client *mantyx.Client, agent AgentSpec, opts ExecutorOptions) (*Executor, error) {
	if client == nil {
		return nil, errors.New("a2asrv: client is required")
	}
	if err := agent.validate(); err != nil {
		return nil, err
	}
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 1024
	}
	return &Executor{
		client:   client,
		agent:    agent,
		opts:     opts,
		sessions: make(map[string]*list.Element),
		lru:      list.New(),
	}, nil
}

// Close ends every cached MANTYX session and frees their MCP transports.
// Safe to call from server shutdown paths and is idempotent.
func (e *Executor) Close(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, elem := range e.sessions {
		entry := elem.Value.(*sessionEntry)
		_ = entry.session.End(ctx)
	}
	e.sessions = make(map[string]*list.Element)
	e.lru = list.New()
}

// Execute implements a2asrv.AgentExecutor.
func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		userText := extractText(execCtx.Message)

		if execCtx.StoredTask == nil {
			initial := a2a.NewSubmittedTask(execCtx, execCtx.Message)
			if !yield(initial, nil) {
				return
			}
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		var deltaEmitErr error
		onDelta := func(delta string) {
			if deltaEmitErr != nil {
				return
			}
			emitOK := true
			if e.opts.OnAssistantDelta != nil {
				e.opts.OnAssistantDelta(execCtx, delta, func(ev a2a.Event) bool {
					if !yield(ev, nil) {
						emitOK = false
						return false
					}
					return true
				})
			} else {
				ev := a2a.NewStatusUpdateEvent(
					execCtx,
					a2a.TaskStateWorking,
					a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart(delta)),
				)
				if !yield(ev, nil) {
					emitOK = false
				}
			}
			if !emitOK {
				deltaEmitErr = context.Canceled
			}
		}

		text, err := e.runOnce(ctx, execCtx.ContextID, userText, onDelta)
		if errors.Is(err, context.Canceled) || errors.Is(deltaEmitErr, context.Canceled) {
			yield(a2a.NewStatusUpdateEvent(
				execCtx,
				a2a.TaskStateCanceled,
				nil,
			), nil)
			return
		}
		if err != nil {
			yield(a2a.NewStatusUpdateEvent(
				execCtx,
				a2a.TaskStateFailed,
				a2a.NewMessageForTask(
					a2a.MessageRoleAgent,
					execCtx,
					a2a.NewTextPart(fmt.Sprintf("MANTYX run failed: %v", err)),
				),
			), nil)
			return
		}

		yield(a2a.NewStatusUpdateEvent(
			execCtx,
			a2a.TaskStateCompleted,
			a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart(text)),
		), nil)
	}
}

// Cancel implements a2asrv.AgentExecutor by emitting a single
// canceled status update. The in-flight Execute call also picks up
// ctx.Done() and exits cooperatively.
func (e *Executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// ----------------------------------------------------------- internals

func (e *Executor) runOnce(
	ctx context.Context,
	contextID, prompt string,
	onDelta func(string),
) (string, error) {
	if e.opts.Conversation == ConversationStateless {
		spec := e.runSpec(prompt)
		spec.OnAssistantDelta = onDelta
		res, err := e.client.RunAgent(ctx, spec)
		if err != nil {
			return "", err
		}
		return res.Text, nil
	}

	session, err := e.getOrCreateSession(ctx, contextID)
	if err != nil {
		return "", err
	}
	res, err := session.Send(ctx, prompt, mantyx.WithAssistantDelta(onDelta))
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func (e *Executor) getOrCreateSession(ctx context.Context, contextID string) (*mantyx.Session, error) {
	e.mu.Lock()
	if elem, ok := e.sessions[contextID]; ok {
		e.lru.MoveToFront(elem)
		s := elem.Value.(*sessionEntry).session
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	spec := e.sessionSpec(contextID)
	session, err := e.client.CreateSession(ctx, spec)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if elem, ok := e.sessions[contextID]; ok {
		// Lost the race — close the session we just created and reuse the
		// existing one.
		go session.End(context.Background()) //nolint:errcheck
		e.lru.MoveToFront(elem)
		return elem.Value.(*sessionEntry).session, nil
	}
	entry := &sessionEntry{contextID: contextID, session: session}
	elem := e.lru.PushFront(entry)
	e.sessions[contextID] = elem
	for e.lru.Len() > e.opts.MaxSessions {
		oldest := e.lru.Back()
		if oldest == nil {
			break
		}
		victim := oldest.Value.(*sessionEntry)
		e.lru.Remove(oldest)
		delete(e.sessions, victim.contextID)
		go victim.session.End(context.Background()) //nolint:errcheck
	}
	return session, nil
}

func (e *Executor) runSpec(prompt string) mantyx.RunSpec {
	return mantyx.RunSpec{
		AgentID:        e.agent.AgentID,
		SystemPrompt:   e.agent.SystemPrompt,
		ModelID:        e.agent.ModelID,
		Name:           e.agent.Name,
		Tools:          e.agent.Tools,
		Prompt:         prompt,
		ReasoningLevel: e.agent.ReasoningLevel,
		OutputSchema:   e.agent.OutputSchema,
		Metadata:       cloneMap(e.agent.Metadata),
	}
}

func (e *Executor) sessionSpec(contextID string) mantyx.SessionSpec {
	meta := cloneMap(e.agent.Metadata)
	if meta == nil {
		meta = map[string]string{}
	}
	if _, ok := meta["a2a_context_id"]; !ok {
		meta["a2a_context_id"] = contextID
	}
	return mantyx.SessionSpec{
		AgentID:        e.agent.AgentID,
		SystemPrompt:   e.agent.SystemPrompt,
		ModelID:        e.agent.ModelID,
		Name:           e.agent.Name,
		Tools:          e.agent.Tools,
		ReasoningLevel: e.agent.ReasoningLevel,
		OutputSchema:   e.agent.OutputSchema,
		Metadata:       meta,
	}
}

func cloneMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func extractText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for i, part := range msg.Parts {
		if part == nil || part.Content == nil {
			continue
		}
		if t, ok := part.Content.(a2a.Text); ok {
			if i > 0 && b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(string(t))
		}
	}
	return b.String()
}

