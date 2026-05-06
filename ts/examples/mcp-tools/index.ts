/**
 * Two flavours of MCP connector in one ephemeral agent:
 *
 *   • `mantyxMcp`     — remote MCP server (Streamable HTTP) MANTYX dials,
 *     lists the catalog of, and proxies. MANTYX prefixes every discovered
 *     tool name as `<server>_<tool>`.
 *   • `defineLocalMcp` — MCP server fully managed by the SDK. Pass either
 *     a Streamable HTTP `url` or an stdio `command`; the SDK opens the
 *     transport, runs `Initialize` + `tools/list` on the first run,
 *     forwards every `local_tool_call` into `tools/call`, and closes the
 *     transport when the run / session ends. The model-facing names are
 *     `<server>_<tool>` regardless of which side resolves the call.
 *
 *   For stdio the SDK spawns the executable; for Streamable HTTP it
 *   speaks JSON-RPC over POST + SSE.
 */
import { MantyxClient, defineLocalMcp, mantyxMcp, type ToolRef } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

const tools: ToolRef[] = [];

if (process.env.GH_MCP_URL) {
  tools.push(
    mantyxMcp({
      name: "github",
      url: process.env.GH_MCP_URL,
      ...(process.env.GH_PAT
        ? { headers: { Authorization: `Bearer ${process.env.GH_PAT}` } }
        : {}),
      ...(process.env.GH_TOOL_FILTER
        ? { toolFilter: process.env.GH_TOOL_FILTER.split(",").map((s) => s.trim()) }
        : {}),
    }),
  );
}

// Local MCP — pick whichever transport your server speaks.
//
//   FS_MCP_URL=http://localhost:8080/mcp          → Streamable HTTP
//   FS_MCP_COMMAND="mcp-server-filesystem ."      → stdio (space-split)
if (process.env.FS_MCP_URL) {
  tools.push(
    defineLocalMcp({
      name: "fs",
      url: process.env.FS_MCP_URL,
      ...(process.env.FS_MCP_TOKEN
        ? { headers: { Authorization: `Bearer ${process.env.FS_MCP_TOKEN}` } }
        : {}),
    }),
  );
} else if (process.env.FS_MCP_COMMAND) {
  const [command, ...args] = process.env.FS_MCP_COMMAND.split(/\s+/).filter(Boolean);
  if (!command) {
    console.error("FS_MCP_COMMAND must be a non-empty command string");
    process.exit(1);
  }
  tools.push(
    defineLocalMcp({
      name: "fs",
      command,
      ...(args.length > 0 ? { args } : {}),
    }),
  );
}

if (tools.length === 0) {
  console.error(
    "Set FS_MCP_URL (Streamable HTTP) or FS_MCP_COMMAND (stdio) — and optionally GH_MCP_URL.",
  );
  process.exit(1);
}

async function main(): Promise<void> {
  const result = await client.runAgent({
    systemPrompt:
      "You are a developer assistant. " +
      "Use `fs_*` tools for the local filesystem and `github_*` tools for repository questions. " +
      "Reply with a short summary.",
    prompt: process.argv[2] ?? "List the first 10 entries of the current working directory.",
    tools,
    reasoningLevel: "low",
    onAssistantDelta: (s) => process.stdout.write(s),
  });
  process.stdout.write("\n---\n");
  console.log("Final reply:", result.text);
}

function required(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`Missing required env var ${name}`);
    process.exit(1);
  }
  return v;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
