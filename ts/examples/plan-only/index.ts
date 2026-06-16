import { MantyxClient } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

async function main(): Promise<void> {
  const prompt =
    process.argv.slice(2).join(" ") ||
    "Migrate billing tables from Postgres 14 to 16 and backfill historical rows.";

  const result = await client.runPlan({
    systemPrompt: "You break complex engineering work into ordered, actionable steps.",
    prompt,
    // Uncomment to skip the classifier and supply your own checklist:
    // brief: "Postgres billing migration",
    // steps: ["Snapshot schema", "Apply DDL", "Backfill rows", "Verify counts"],
    onEvent: (ev) => {
      if (ev.type === "task_plan") {
        console.log("\n[task_plan]", ev.brief ?? "(classifying…)");
        for (const step of ev.steps) {
          console.log(`  [${step.status}] ${step.title}`);
        }
      }
    },
  });

  console.log("\n---\nSummary:\n", result.text);
  if (result.plan) {
    console.log("\nStructured plan:");
    if (result.plan.brief) console.log("Brief:", result.plan.brief);
    for (const step of result.plan.steps) {
      console.log(`  [${step.status}] ${step.title}`);
    }
  } else {
    console.log("\n(No multi-step plan — classifier declined.)");
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
