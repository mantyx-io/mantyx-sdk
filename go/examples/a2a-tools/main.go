// Two flavours of Agent2Agent delegation in one ephemeral agent:
//
//   • mantyx.MantyxA2A — public peer MANTYX dials directly (server-resolved).
//   • mantyx.LocalA2A  — peer the SDK reaches on MANTYX's behalf
//     (client-resolved). Pass the Agent Card URL only — the SDK fetches
//     the card on the first run, ships it inline with the spec (so
//     MANTYX never reaches your intranet), and JSON-RPC `message/send`s
//     against the resolved card's `url` whenever MANTYX emits a
//     `local_tool_call` event for this tool.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	mantyx "github.com/mantyx-io/mantyx-go-sdk"
)

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	tools := []mantyx.ToolRef{}

	if cardURL := os.Getenv("BILLING_AGENT_CARD_URL"); cardURL != "" {
		opts := mantyx.MantyxA2AOptions{
			Name:         "billing_agent",
			Description:  "Delegate billing questions to the public Acme billing agent.",
			AgentCardURL: cardURL,
		}
		if token := os.Getenv("BILLING_AGENT_TOKEN"); token != "" {
			opts.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		tools = append(tools, mantyx.MantyxA2A(opts))
	}

	if cardURL := os.Getenv("HR_AGENT_CARD_URL"); cardURL != "" {
		spec := mantyx.LocalA2ASpec{
			Name:         "intranet_hr_agent",
			AgentCardURL: cardURL,
		}
		if token := os.Getenv("HR_AGENT_TOKEN"); token != "" {
			spec.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		tools = append(tools, mantyx.LocalA2A(spec))
	}

	if len(tools) == 0 {
		fmt.Fprintln(os.Stderr, "Set HR_AGENT_CARD_URL (and optionally BILLING_AGENT_CARD_URL) to a reachable Agent Card endpoint.")
		os.Exit(1)
	}

	prompt := "When does the company holiday calendar reset for the new fiscal year?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You are a helpful router. " +
			"Use `billing_agent` for billing questions and `intranet_hr_agent` for HR / time-off questions. " +
			"If only one delegate is available, fall back to it. Reply with the delegate's answer.",
		Prompt:         prompt,
		Tools:          tools,
		ReasoningLevel: mantyx.ReasoningMedium(),
		OnAssistantDelta: func(s string) {
			fmt.Print(s)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println("Final reply:", result.Text)
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
