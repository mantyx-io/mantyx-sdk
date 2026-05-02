package main

import (
	"context"
	"fmt"
	"log"
	"os"

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

	ctx := context.Background()
	catalog, err := client.ListModels(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Available models:")
	for _, m := range catalog.Models {
		fmt.Printf("  %-40s  %s  (%s/%s)\n", m.ID, m.Label, m.Provider, m.VendorModelID)
	}
	if catalog.DefaultModelID != "" {
		fmt.Println("Default:", catalog.DefaultModelID)
	}
	if len(catalog.Models) == 0 {
		fmt.Println("\nNo models available; configure an LLM provider in MANTYX first.")
		return
	}
	first := catalog.Models[0]
	fmt.Printf("\nRunning a one-shot on %s...\n", first.ID)
	out, err := client.RunAgent(ctx, mantyx.RunSpec{
		SystemPrompt: "You answer in one short sentence.",
		Prompt:       "What is the capital of France?",
		ModelID:      first.ID,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Reply:", out.Text)
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
