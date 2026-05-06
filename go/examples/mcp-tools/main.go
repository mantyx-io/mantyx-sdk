// Two flavours of MCP connector in one ephemeral agent:
//
//   • mantyx.MantyxMcp — remote MCP server (Streamable HTTP) MANTYX dials,
//     lists the catalog of, and proxies. MANTYX prefixes every discovered
//     tool name as `<server>_<tool>`.
//   • mantyx.LocalMcp  — MCP server fully managed by the SDK. Pass either
//     a Streamable HTTP URL (+ optional Headers) or an stdio Command (+
//     Args/Env/Cwd); the SDK opens the transport, runs Initialize +
//     tools/list on the first run, ships the resolved catalog inline so
//     MANTYX can render the tools to the model under `<server>_<tool>`,
//     forwards every `local_tool_call` to the live MCP session via
//     tools/call, and closes the transport when the run / session ends.
//     Powered by the official `github.com/modelcontextprotocol/go-sdk`.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

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

	tools := []mantyx.ToolRef{}

	// Local MCP — pick whichever transport your server speaks.
	if mcpURL := os.Getenv("FS_MCP_URL"); mcpURL != "" {
		spec := mantyx.LocalMcpSpec{Name: "fs", URL: mcpURL}
		if token := os.Getenv("FS_MCP_TOKEN"); token != "" {
			spec.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		tools = append(tools, mantyx.LocalMcp(spec))
	} else if cmd := os.Getenv("FS_MCP_COMMAND"); cmd != "" {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			log.Fatalf("FS_MCP_COMMAND must be a non-empty command string")
		}
		tools = append(tools, mantyx.LocalMcp(mantyx.LocalMcpSpec{
			Name:    "fs",
			Command: parts[0],
			Args:    parts[1:],
		}))
	}

	if mcpURL := os.Getenv("GH_MCP_URL"); mcpURL != "" {
		mcpOpts := mantyx.MantyxMcpOptions{Name: "github", URL: mcpURL}
		if pat := os.Getenv("GH_PAT"); pat != "" {
			mcpOpts.Headers = map[string]string{"Authorization": "Bearer " + pat}
		}
		if filter := os.Getenv("GH_TOOL_FILTER"); filter != "" {
			parts := strings.Split(filter, ",")
			cleaned := make([]string, 0, len(parts))
			for _, p := range parts {
				if v := strings.TrimSpace(p); v != "" {
					cleaned = append(cleaned, v)
				}
			}
			mcpOpts.ToolFilter = cleaned
		}
		tools = append(tools, mantyx.MantyxMcp(mcpOpts))
	}

	if len(tools) == 0 {
		fmt.Fprintln(os.Stderr,
			"Set FS_MCP_URL (Streamable HTTP) or FS_MCP_COMMAND (stdio) — and optionally GH_MCP_URL.")
		os.Exit(1)
	}

	prompt := "List the first 10 entries of the current working directory."
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
		SystemPrompt: "You are a developer assistant. " +
			"Use `fs_*` tools for the local filesystem and `github_*` tools for repository questions. " +
			"Reply with a short summary.",
		Prompt:         prompt,
		Tools:          tools,
		ReasoningLevel: mantyx.ReasoningLow(),
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
