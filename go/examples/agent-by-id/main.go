// Trigger a persisted MANTYX agent (by id) and let it call back into this
// process via a `local` tool.
//
// The agent's stored system prompt, model, and server-side tools (memory,
// skills, plugin tools, …) are loaded from the workspace at run time. The
// Tools slice on RunSpec is merged on top — typically LocalTool refs the
// agent should be able to call back into for that run.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

type readFileArgs struct {
	Path string `json:"path" jsonschema:"Path to the file to read"`
}

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")
	agentID := mustEnv("MANTYX_AGENT_ID")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	tool := mantyx.LocalTool(mantyx.LocalToolSpec{
		Name:        "read_local_file",
		Description: "Read a UTF-8 file from the caller's local filesystem.",
		Parameters:  &readFileArgs{},
		Execute: func(ctx context.Context, args readFileArgs) (string, error) {
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		AgentID: agentID,
		Prompt:  "Inspect /etc/hostname using read_local_file and tell me what hostname this box has.",
		Tools:   []mantyx.ToolRef{tool},
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
