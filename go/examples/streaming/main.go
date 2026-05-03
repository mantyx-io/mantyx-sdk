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

	stream, err := client.StreamAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You are a haiku poet.",
		Prompt:       "Write a haiku about ephemeral agents.",
	})
	if err != nil {
		log.Fatal(err)
	}
	for ev := range stream {
		switch ev.Type {
		case "assistant_delta":
			if t, ok := ev.Data["text"].(string); ok {
				fmt.Print(t)
			}
		case "result":
			fmt.Println()
			fmt.Println("---")
			fmt.Printf("done: %+v\n", ev.Data)
		default:
			fmt.Println()
			fmt.Printf("[event] %s %+v\n", ev.Type, ev.Data)
		}
	}
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
