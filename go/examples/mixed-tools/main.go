package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	mantyx "github.com/mantyx/mantyx-go-sdk"
)

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")
	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	tools := []mantyx.ToolRef{
		mantyx.LocalTool(mantyx.LocalToolSpec{
			Name:        "current_time",
			Description: "Return the current ISO timestamp from the developer's local clock.",
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				return time.Now().UTC().Format(time.RFC3339), nil
			},
		}),
	}
	if id := os.Getenv("MANTYX_TOOL_ID"); id != "" {
		tools = append(tools, mantyx.MantyxTool(id))
	}
	if name := os.Getenv("MANTYX_PLUGIN_TOOL_NAME"); name != "" {
		tools = append(tools, mantyx.MantyxPluginTool(name))
	}

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You are a tool-using assistant. Choose any tool that helps; otherwise reply directly.",
		Prompt:       "What time is it on the developer's machine, and what tools do you have?",
		Tools:        tools,
		OnAssistantDelta: func(s string) {
			fmt.Print(s)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Printf("runId=%s text=%q\n", result.RunID, result.Text)
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
