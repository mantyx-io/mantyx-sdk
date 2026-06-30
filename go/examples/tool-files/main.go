// Local tool that returns a file alongside its textual result.
//
// render_bar_chart renders a dependency-free SVG chart in this process and
// hands it back to the model via mantyx.ToolResult. MANTYX surfaces the bytes
// to the model on its next turn as a native file part, so the model can "see"
// the image it just asked for.
//
// Returning a ToolResult also skips output-schema inference — the
// {result, files} envelope is transport, not a model-facing output contract.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

type chartArgs struct {
	Title  string    `json:"title" jsonschema:"Chart title shown above the bars"`
	Values []float64 `json:"values" jsonschema:"The bar heights, left to right"`
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
		Name:        "render_bar_chart",
		Description: "Render a labelled bar chart from a series of numbers and return it as an SVG image.",
		Parameters:  &chartArgs{},
		Execute: func(ctx context.Context, args chartArgs) (mantyx.ToolResult, error) {
			svg := barChartSVG(args.Title, args.Values)
			return mantyx.ToolResult{
				Result: fmt.Sprintf("Rendered %q as a %d-bar chart (max value %g).",
					args.Title, len(args.Values), maxOf(args.Values)),
				Files: []mantyx.ToolResultFile{
					{
						Filename: "chart.svg",
						MimeType: "image/svg+xml",
						Data:     base64.StdEncoding.EncodeToString([]byte(svg)),
					},
				},
			}, nil
		},
	})

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You are a data-viz assistant. When asked for a chart, call render_bar_chart, " +
			"then describe the resulting image in one sentence.",
		Prompt: `Plot our weekly sales — 12, 19, 8, 15, 22 — titled "Weekly Sales" and describe the trend.`,
		Tools:  []mantyx.ToolRef{tool},
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

// barChartSVG builds a minimal dependency-free SVG bar chart.
func barChartSVG(title string, values []float64) string {
	const barW, gap, padX, padTop, chartH = 48, 16, 24, 48, 180
	maxV := math.Max(maxOf(values), 1)
	width := padX*2 + len(values)*barW + (len(values)-1)*gap
	height := padTop + chartH + 32

	var bars strings.Builder
	for i, v := range values {
		h := int(math.Round((v / maxV) * chartH))
		x := padX + i*(barW+gap)
		y := padTop + (chartH - h)
		fmt.Fprintf(&bars,
			`<rect x="%d" y="%d" width="%d" height="%d" rx="4" fill="#4f46e5" />`+
				`<text x="%d" y="%d" font-size="13" text-anchor="middle" fill="#111">%g</text>`,
			x, y, barW, h, x+barW/2, y-6, v)
	}

	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+
			`<rect width="100%%" height="100%%" fill="#fff" />`+
			`<text x="%d" y="28" font-size="18" font-weight="600" text-anchor="middle" fill="#111">%s</text>`+
			`%s</svg>`,
		width, height, width, height, width/2, escapeXML(title), bars.String())
}

func maxOf(values []float64) float64 {
	m := math.Inf(-1)
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	if math.IsInf(m, -1) {
		return 0
	}
	return m
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing required env var %s\n", name)
		os.Exit(1)
	}
	return v
}
