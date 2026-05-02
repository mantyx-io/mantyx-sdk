import { readFile } from "node:fs/promises";
import { z } from "zod";
import { MantyxClient, defineLocalTool } from "@mantyx/sdk";

/**
 * Trigger a persisted MANTYX agent (by id) and let it call back into this
 * process via a `local` tool.
 *
 * The agent's stored system prompt, model, and server-side tools (memory,
 * skills, plugin tools, …) are loaded from the workspace at run time. The
 * `tools` array on `runAgent` is merged on top — typically `local` tools
 * the agent should be able to call back into for that run.
 */

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");
const agentId = required("MANTYX_AGENT_ID");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

const readFileTool = defineLocalTool({
  name: "read_local_file",
  description: "Read a UTF-8 file from the caller's local filesystem.",
  parameters: z.object({
    path: z.string().describe("Absolute or relative path to the file."),
  }),
  execute: async ({ path }) => readFile(path, "utf8"),
});

async function main(): Promise<void> {
  const result = await client.runAgent({
    agentId,
    prompt:
      "Inspect /etc/hostname using read_local_file and tell me what hostname this box has.",
    tools: [readFileTool],
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
