import { z } from "zod";
import {
  MantyxClient,
  defineLocalTool,
  mantyxPluginTool,
  mantyxTool,
  type ToolRef,
} from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

const tools: ToolRef[] = [
  defineLocalTool({
    name: "current_time",
    description: "Return the current ISO timestamp from the developer's local clock.",
    parameters: z.object({}),
    execute: () => new Date().toISOString(),
  }),
];

if (process.env.MANTYX_TOOL_ID) {
  tools.push(mantyxTool(process.env.MANTYX_TOOL_ID));
}
if (process.env.MANTYX_PLUGIN_TOOL_NAME) {
  tools.push(mantyxPluginTool(process.env.MANTYX_PLUGIN_TOOL_NAME));
}

async function main(): Promise<void> {
  const result = await client.runAgent({
    systemPrompt:
      "You are a tool-using assistant. Choose any tool that helps; otherwise reply directly.",
    prompt: "What time is it on the developer's machine, and what tools do you have?",
    tools,
    onAssistantDelta: (s) => process.stdout.write(s),
  });
  process.stdout.write("\n---\n");
  console.log(JSON.stringify({ runId: result.runId, finalText: result.text }, null, 2));
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
