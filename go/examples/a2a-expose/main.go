// Expose a MANTYX agent over the Agent2Agent (A2A) protocol.
//
// Spins up an A2A server on http://localhost:4000 backed by a MANTYX agent
// (ephemeral or persisted). Other A2A agents can discover it at:
//
//	GET http://localhost:4000/.well-known/agent-card.json
//
// …and call it over JSON-RPC at the root path. Each unique A2A contextID
// maps to a long-lived MANTYX session by default, so multi-turn
// conversations share history without any extra plumbing.
//
// Usage:
//
//	export MANTYX_API_KEY=mtx_live_...
//	export MANTYX_WORKSPACE_SLUG=acme-corp
//	# Either point at a persisted MANTYX agent…
//	export MANTYX_AGENT_ID=agent_cm6abc123
//	# …or rely on the default ephemeral system prompt.
//	# export SYSTEM_PROMPT="You are a billing assistant."
//	go run .
//
// Probe it from another shell:
//
//	curl http://localhost:4000/.well-known/agent-card.json | jq .
//	curl -X POST http://localhost:4000/ -H "content-type: application/json" \
//	  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"u1","role":"ROLE_USER","parts":[{"text":"Hi!"}]}}}' | jq .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	mantyx "github.com/mantyx-io/mantyx-go-sdk"
	"github.com/mantyx-io/mantyx-go-sdk/a2asrv"
)

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	port := envOr("PORT", "4000")
	publicURL := envOr("PUBLIC_URL", "http://localhost:"+port)

	card := a2asrv.NewSimpleAgentCard(
		envOr("AGENT_NAME", "MANTYX Demo Agent"),
		envOr("AGENT_DESCRIPTION", "A MANTYX agent exposed as an Agent2Agent peer."),
		"1.0.0",
		publicURL,
	)

	agent := a2asrv.AgentSpec{}
	if id := os.Getenv("MANTYX_AGENT_ID"); id != "" {
		agent.AgentID = id
	} else {
		agent.SystemPrompt = envOr("SYSTEM_PROMPT",
			"You are a friendly MANTYX assistant. Keep replies concise.")
		agent.ModelID = os.Getenv("MODEL_ID")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handle, err := a2asrv.Serve(ctx, a2asrv.ServeOptions{
		Client:    client,
		Agent:     agent,
		AgentCard: card,
		Addr:      ":" + port,
	})
	if err != nil {
		log.Fatalf("Serve: %v", err)
	}

	fmt.Printf("MANTYX agent live at %s\n", handle.URL)
	fmt.Printf("Agent Card:    %s/.well-known/agent-card.json\n", handle.URL)
	fmt.Printf("JSON-RPC:      %s/\n", handle.URL)
	fmt.Printf("HTTP+JSON:     %s/v1/\n", handle.URL)

	go func() {
		if err := handle.Wait(); err != nil {
			log.Printf("server stopped: %v", err)
		}
	}()
	<-ctx.Done()
	fmt.Println("\nShutting down…")
	if err := handle.Close(context.Background()); err != nil {
		log.Printf("close: %v", err)
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing required env var: %s", k)
	}
	return v
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
