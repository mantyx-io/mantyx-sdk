// Package mantyx provides a Go client for the MANTYX agent runtime.
//
// MANTYX is an "agent operating system" — a hosted runtime that drives the
// LLM loop and resolves server-side tools. This SDK lets a developer mix
// MANTYX-managed tools with locally executed tools in their own process,
// without giving up the convenience of a managed runtime.
//
// Quick start:
//
//	client := mantyx.NewClient(mantyx.Options{
//	    APIKey:        os.Getenv("MANTYX_API_KEY"),
//	    WorkspaceSlug: os.Getenv("MANTYX_WORKSPACE_SLUG"),
//	})
//
//	result, err := client.RunAgent(ctx, mantyx.RunSpec{
//	    SystemPrompt: "You are a helpful assistant.",
//	    Prompt:       "Read /etc/hostname and tell me what it says.",
//	    Tools: []mantyx.ToolRef{
//	        mantyx.LocalTool(mantyx.LocalToolSpec{
//	            Name:        "read_file",
//	            Description: "Read a UTF-8 file from the local filesystem.",
//	            Parameters:  &readFileArgs{},
//	            Execute: func(ctx context.Context, args readFileArgs) (string, error) {
//	                data, err := os.ReadFile(args.Path)
//	                if err != nil {
//	                    return "", err
//	                }
//	                return string(data), nil
//	            },
//	        }),
//	    },
//	})
//
// The wire protocol (HTTP + Server-Sent Events) is documented in
// docs/agent-runs-protocol.md.
package mantyx
