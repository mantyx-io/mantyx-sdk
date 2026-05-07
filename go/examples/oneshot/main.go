package main

import (
	"context"
	"fmt"
	"log"
	"os"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

type readFileArgs struct {
	Path string `json:"path" jsonschema:"description=Path to the file to read"`
}

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	tool := mantyx.LocalTool(mantyx.LocalToolSpec{
		Name:        "read_file",
		Description: "Read a UTF-8 file from the local filesystem.",
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
		SystemPrompt: "You are a code review assistant. Use read_file when asked.",
		Prompt:       "Read /etc/hostname and tell me what it says in one sentence.",
		Tools:        []mantyx.ToolRef{tool},
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
