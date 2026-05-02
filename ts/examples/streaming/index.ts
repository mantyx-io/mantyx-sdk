import { MantyxClient } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

async function main(): Promise<void> {
  const stream = client.streamAgent({
    systemPrompt: "You are a haiku poet.",
    prompt: "Write a haiku about ephemeral agents.",
  });
  for await (const event of stream) {
    if (event.type === "assistant_delta") {
      process.stdout.write((event as { text: string }).text);
    } else if (event.type === "result") {
      process.stdout.write("\n---\n");
      console.log("done:", JSON.stringify(event));
    } else {
      console.log("\n[event]", event.type, JSON.stringify(event));
    }
  }
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
