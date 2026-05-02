package main

import (
	"bufio"
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
	session, err := client.CreateSession(ctx, mantyx.SessionSpec{
		Name:         "repl",
		SystemPrompt: "You are a friendly chat assistant. Keep replies concise.",
		// Tag every run in this session so they can be filtered in the MANTYX
		// dashboard (Agent runs → Sessions, "metadata" filter).
		Metadata: map[string]string{
			"example": "session-chat",
			"env":     envOr("APP_ENV", "development"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer session.End(ctx) //nolint:errcheck
	fmt.Printf("Session created (%s). Type messages, Ctrl+D to exit.\n", session.ID)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return
		}
		line = trimNewline(line)
		if line == "" {
			continue
		}
		_, err = session.Send(ctx, line, mantyx.WithAssistantDelta(func(s string) {
			fmt.Print(s)
		}))
		fmt.Println()
		if err != nil {
			fmt.Println("[run failed]", err)
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

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
