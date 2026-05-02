import { MantyxClient } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

async function main(): Promise<void> {
  const catalog = await client.listModels();
  console.log("Available models:");
  for (const m of catalog.models) {
    console.log(`  ${m.id.padEnd(40)}  ${m.label}  (${m.provider}/${m.vendorModelId})`);
  }
  console.log(`Default: ${catalog.defaultModelId ?? "(none configured)"}`);

  const first = catalog.models[0];
  if (!first) {
    console.error("\nNo models available; configure an LLM provider in MANTYX first.");
    return;
  }
  console.log(`\nRunning a one-shot on ${first.id}...`);
  const result = await client.runAgent({
    systemPrompt: "You answer in one short sentence.",
    prompt: "What is the capital of France?",
    modelId: first.id,
  });
  console.log("Reply:", result.text);
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
