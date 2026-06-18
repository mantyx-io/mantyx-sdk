package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

func main() {
	mode := "one-shot"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	attachment, err := loadSampleAttachment()
	if err != nil {
		log.Fatal(err)
	}

	switch mode {
	case "one-shot":
		runOneShot(client, attachment)
	case "messages":
		runMessages(client, attachment)
	case "session":
		runSession(client, attachment)
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode %q; use one-shot, messages, or session\n", mode)
		os.Exit(1)
	}
}

func loadSampleAttachment() (map[string]any, error) {
	data, err := os.ReadFile("sample.txt")
	if err != nil {
		return nil, err
	}
	return mantyx.InputFileAttachment("text/plain", "sample.txt", base64.StdEncoding.EncodeToString(data)), nil
}

func runOneShot(client *mantyx.Client, attachment map[string]any) {
	fmt.Println("=== one-shot: prompt + attachments ===")
	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You summarize uploaded text files in two short bullets.",
		Prompt:       "What are the action items in the attached notes?",
		Attachments:  []map[string]any{attachment},
		OnAssistantDelta: func(s string) {
			fmt.Print(s)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println(result.Text)
}

func runMessages(client *mantyx.Client, attachment map[string]any) {
	fmt.Println("=== one-shot: explicit messages array ===")
	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		Messages: []mantyx.Message{
			{Role: "system", Content: "You summarize uploaded text files in two short bullets."},
			{Role: "user", Content: "Earlier: we discussed Q2 planning."},
			{Role: "assistant", Content: "Got it — send the file when you're ready."},
			{
				Role:        "user",
				Content:     "What's in the attached notes?",
				Attachments: []map[string]any{attachment},
			},
		},
		OnAssistantDelta: func(s string) {
			fmt.Print(s)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println(result.Text)
}

func runSession(client *mantyx.Client, attachment map[string]any) {
	fmt.Println("=== session: send with attachments ===")
	ctx := context.Background()
	session, err := client.CreateSession(ctx, mantyx.SessionSpec{
		SystemPrompt: "You summarize uploaded text files in two short bullets.",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = session.End(ctx) }()

	result, err := session.Send(ctx, "Summarize the attached planning notes.",
		mantyx.WithAttachments(attachment),
		mantyx.WithAssistantDelta(func(s string) { fmt.Print(s) }),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println(result.Text)

	events, err := session.Events(ctx, mantyx.GetSessionEventsOptions{LastMessages: 2})
	if err != nil {
		log.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == "user_message" {
			if attachments, ok := ev.Data["attachments"]; ok {
				fmt.Println("Replay metadata:", attachments)
			}
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
