package a2asrv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

// ServeOptions configures Serve.
type ServeOptions struct {
	// Client is the MANTYX client backing the A2A executor (required).
	Client *mantyx.Client
	// Agent describes which MANTYX agent to expose (required).
	Agent AgentSpec
	// AgentCard published at /.well-known/agent-card.json (required).
	AgentCard *a2a.AgentCard
	// Addr is the TCP address to listen on, e.g. ":4000". When empty,
	// listens on a random free port (use Handle.Addr to discover it).
	Addr string
	// JSONRPCPath is the mount path for the JSON-RPC endpoint. Defaults
	// to "/" (root).
	JSONRPCPath string
	// AgentCardPath overrides the default well-known path.
	AgentCardPath string
	// DisableREST hides the HTTP+JSON/REST endpoint when true.
	DisableREST bool
	// Conversation forwarded to NewExecutor.
	Conversation Conversation
	// MaxSessions forwarded to NewExecutor.
	MaxSessions int
	// OnAssistantDelta forwarded to NewExecutor.
	OnAssistantDelta func(execCtx *a2asrv.ExecutorContext, delta string, yield func(a2a.Event) bool)
	// HandlerOptions are appended to NewHandler. Useful for plugging in
	// a custom task store, push notification stack, etc.
	HandlerOptions []a2asrv.RequestHandlerOption
}

// ServeHandle is the result of Serve.
type ServeHandle struct {
	// URL is the externally-reachable origin (best-effort) of the bound
	// listener, e.g. "http://localhost:4000".
	URL string
	// Addr is the actual TCP address the server bound to.
	Addr string
	// Executor is the MANTYX-backed executor wired into the server.
	Executor *Executor

	server   *http.Server
	listener net.Listener
	errCh    chan error
}

// Wait blocks until the server stops and returns any startup / shutdown
// error that wasn't a clean http.ErrServerClosed.
func (h *ServeHandle) Wait() error {
	if h == nil || h.errCh == nil {
		return nil
	}
	err := <-h.errCh
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close shuts down the HTTP listener and ends every cached MANTYX session.
// Safe to call multiple times.
func (h *ServeHandle) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	var firstErr error
	if h.server != nil {
		if err := h.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			firstErr = err
		}
	}
	if h.Executor != nil {
		h.Executor.Close(ctx)
	}
	return firstErr
}

// Serve spins up a small net/http listener that exposes a MANTYX agent as
// an A2A peer. The returned handle owns the underlying listener and the
// MANTYX-backed executor; call Close on shutdown. For production
// deployments mount the executor in your own server using NewExecutor +
// a2asrv.NewHandler instead.
func Serve(ctx context.Context, opts ServeOptions) (*ServeHandle, error) {
	if opts.Client == nil {
		return nil, errors.New("a2asrv.Serve: Client is required")
	}
	if opts.AgentCard == nil {
		return nil, errors.New("a2asrv.Serve: AgentCard is required")
	}

	executor, err := NewExecutor(opts.Client, opts.Agent, ExecutorOptions{
		Conversation:     opts.Conversation,
		MaxSessions:      opts.MaxSessions,
		OnAssistantDelta: opts.OnAssistantDelta,
	})
	if err != nil {
		return nil, err
	}

	requestHandler := a2asrv.NewHandler(executor, opts.HandlerOptions...)

	cardPath := opts.AgentCardPath
	if cardPath == "" {
		cardPath = a2asrv.WellKnownAgentCardPath
	}
	rpcPath := opts.JSONRPCPath
	if rpcPath == "" {
		rpcPath = "/"
	}

	mux := http.NewServeMux()
	mux.Handle(cardPath, a2asrv.NewStaticAgentCardHandler(opts.AgentCard))
	if !opts.DisableREST {
		// The REST handler exposes /v1/message:send, /v1/message:stream, etc.
		mux.Handle("/v1/", a2asrv.NewRESTHandler(requestHandler))
	}
	mux.Handle(rpcPath, a2asrv.NewJSONRPCHandler(requestHandler))

	addr := opts.Addr
	if addr == "" {
		addr = ":0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		executor.Close(ctx)
		return nil, fmt.Errorf("a2asrv.Serve: listen %s: %w", addr, err)
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ln)
	}()

	return &ServeHandle{
		URL:      buildDisplayURL(ln.Addr().String()),
		Addr:     ln.Addr().String(),
		Executor: executor,
		server:   server,
		listener: ln,
		errCh:    errCh,
	}, nil
}

func buildDisplayURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// NewSimpleAgentCard is a tiny helper for callers who don't need to
// hand-roll the full a2a.AgentCard struct. It populates the fields most
// users want and leaves the rest at their zero values; mutate the result
// before passing it to Serve if you need provider, security, or extension
// metadata.
func NewSimpleAgentCard(name, description, version, publicURL string, skills ...a2a.AgentSkill) *a2a.AgentCard {
	if len(skills) == 0 {
		skills = []a2a.AgentSkill{
			{ID: "chat", Name: "Chat", Description: description, Tags: []string{"chat"}},
		}
	}
	return &a2a.AgentCard{
		Name:        name,
		Description: description,
		Version:     version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(strings.TrimRight(publicURL, "/"), a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		Skills:             skills,
	}
}
