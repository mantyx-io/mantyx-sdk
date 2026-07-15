package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")

	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	prompt := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	if prompt == "" {
		prompt = "Migrate billing tables from Postgres 14 to 16 and backfill historical rows."
	}

	out, err := client.RunPlan(context.Background(), mantyx.RunPlanSpec{
		RunSpec: mantyx.RunSpec{
			SystemPrompt: "You break complex engineering work into ordered, actionable steps.",
			Prompt:       prompt,
			OnEvent: func(ev mantyx.RunEvent) {
				if ev.Type != "task_plan" {
					return
				}
				plan := mantyx.TaskPlanFromEventData(ev.Data)
				if plan == nil {
					return
				}
				brief := plan.Brief
				if brief == "" {
					brief = "(planning…)"
				}
				fmt.Println("\n[task_plan]", brief)
				for _, step := range plan.Steps {
					fmt.Printf("  [%s] %s\n", step.Status, step.Title)
				}
			},
		},
		// Uncomment to supply your own checklist:
		// Brief: "Postgres billing migration",
		// Steps: []string{"Snapshot schema", "Apply DDL", "Backfill rows", "Verify counts"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n---\nSummary:\n", out.Text)
	if out.Plan == nil || len(out.Plan.Steps) == 0 {
		fmt.Println("\n(No structured plan returned.)")
		return
	}
	fmt.Println("\nStructured plan:")
	if out.Plan.Brief != "" {
		fmt.Println("Brief:", out.Plan.Brief)
	}
	for _, step := range out.Plan.Steps {
		fmt.Printf("  [%s] %s\n", step.Status, step.Title)
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
