// Constrain the model's final reply to a JSON document matching a JSON Schema
// and decode it into a typed Go struct via mantyx.ParseRunOutput.
//
// The model is asked for a structured weather report. MANTYX forwards the
// schema to the provider (OpenAI Responses, Gemini ≥ 2.5, Anthropic synthetic
// final_report tool). Strict enforcement makes provider rejection, unsupported
// models, and unconstrained fallback fail explicitly.
//
// The schema itself is reflected from the WeatherReport struct via
// google/jsonschema-go — the same path LocalToolSpec.Parameters uses — so
// there is exactly one source of truth for the shape and there is no
// hand-written `map[string]any` schema to keep in sync.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

// WeatherReport is both the typed shape we want the model to return *and*
// the source of the JSON Schema MANTYX forwards to the provider. Per-field
// `jsonschema:"..."` tags become property `description`s in the schema.
type WeatherReport struct {
	City         string  `json:"city" jsonschema:"City the report is for"`
	TemperatureC float64 `json:"temperature_c" jsonschema:"Current temperature in Celsius"`
	Conditions   string  `json:"conditions" jsonschema:"One short clause describing the weather (e.g. \"clear and crisp\")"`
}

func main() {
	apiKey := mustEnv("MANTYX_API_KEY")
	workspace := mustEnv("MANTYX_WORKSPACE_SLUG")
	opts := mantyx.Options{APIKey: apiKey, WorkspaceSlug: workspace}
	if base := os.Getenv("MANTYX_BASE_URL"); base != "" {
		opts.BaseURL = base
	}
	client := mantyx.NewClient(opts)

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You return weather reports as JSON conforming to the response schema.",
		Prompt:       "What's the weather in San Francisco right now? Make up plausible numbers.",
		OutputSchema: &mantyx.OutputSchema{
			Name:        "weather_report",
			Schema:      &WeatherReport{},
			Enforcement: mantyx.OutputSchemaEnforcementStrict,
		},
	})
	if err != nil {
		var runErr *mantyx.RunError
		if errors.As(err, &runErr) {
			log.Fatalf(
				"structured output failed: class=%s provider_status=%d provider_code=%s message=%s",
				runErr.ErrorClass,
				runErr.APIStatus,
				runErr.APICode,
				runErr.Message,
			)
		}
		log.Fatal(err)
	}

	if result.StructuredOutput == nil ||
		!result.StructuredOutput.SchemaRequested ||
		!result.StructuredOutput.SchemaEnforced ||
		result.StructuredOutput.UnconstrainedFallbackOccurred {
		log.Fatal("MANTYX did not report structured-output enforcement")
	}
	fmt.Println("Raw model reply:", result.Text)

	var report WeatherReport
	if err := mantyx.ParseRunOutput(result, &report); err != nil {
		var pe *mantyx.ParseError
		if errors.As(err, &pe) {
			log.Fatalf("model returned non-conformant text (run %s): %q", pe.RunID, pe.Text)
		}
		log.Fatal(err)
	}

	fmt.Println("---")
	fmt.Printf("City:        %s\n", report.City)
	fmt.Printf("Temperature: %.1f°C\n", report.TemperatureC)
	fmt.Printf("Conditions:  %s\n", report.Conditions)
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
