import { z } from "zod";
import { MantyxClient, defineLocalTool } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

const randomOddTool = defineLocalTool({
  name: "random_odd",
  description: "Generate a random odd number between 1 and 100.",
  parameters: z.object({}),
  execute: async (): Promise<string> => {
    return String(Math.floor(Math.random() * 50) * 2 + 1);
  },
});

async function main(): Promise<void> {
  const result = await client.runAgent({
    systemPrompt:
      "You are a helpful assistant. Use the random_odd tool to generate a random odd number between 1 and 100.",
    prompt: "Generate a random odd number between 1 and 100.",
    tools: [randomOddTool],
    onAssistantDelta: (s: string) => process.stdout.write(s),
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
