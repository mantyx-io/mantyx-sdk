/**
 * Two flavours of Agent2Agent delegation in one ephemeral agent:
 *
 *   • `mantyxA2A`     — public peer MANTYX dials directly (server-resolved).
 *   • `defineLocalA2A` — intranet peer fully resolved by the SDK
 *     (client-resolved). Pass an `agentCardUrl`; the SDK fetches the
 *     Agent Card on the first run, ships it inline, and speaks A2A
 *     `message/send` to `agentCard.url` whenever MANTYX emits a
 *     `local_tool_call` for this tool. You only supply the URL.
 */
import { MantyxClient, defineLocalA2A, mantyxA2A, type ToolRef } from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

async function main(): Promise<void> {
  const tools: ToolRef[] = [];

  if (process.env.BILLING_AGENT_CARD_URL) {
    tools.push(
      mantyxA2A({
        name: "billing_agent",
        description: "Delegate billing questions to the public Acme billing agent.",
        agentCardUrl: process.env.BILLING_AGENT_CARD_URL,
        ...(process.env.BILLING_AGENT_TOKEN
          ? { headers: { Authorization: `Bearer ${process.env.BILLING_AGENT_TOKEN}` } }
          : {}),
      }),
    );
  }

  // Local intranet peer — pass the Agent Card URL only. The SDK fetches
  // the card at spec-submit time, caches it for the run, and uses
  // `agentCard.url` as the `message/send` target.
  if (process.env.HR_AGENT_CARD_URL) {
    tools.push(
      defineLocalA2A({
        name: "intranet_hr_agent",
        agentCardUrl: process.env.HR_AGENT_CARD_URL,
        ...(process.env.HR_AGENT_TOKEN
          ? { headers: { Authorization: `Bearer ${process.env.HR_AGENT_TOKEN}` } }
          : {}),
      }),
    );
  }

  if (tools.length === 0) {
    console.error(
      "Set HR_AGENT_CARD_URL (and optionally BILLING_AGENT_CARD_URL) to a reachable Agent Card endpoint.",
    );
    process.exit(1);
  }

  const result = await client.runAgent({
    systemPrompt:
      "You are a helpful router. " +
      "Use `billing_agent` for billing questions and `intranet_hr_agent` for HR / time-off questions. " +
      "If only one delegate is available, fall back to it. Reply with the delegate's answer.",
    prompt:
      process.argv[2] ?? "When does the company holiday calendar reset for the new fiscal year?",
    tools,
    reasoningLevel: "medium",
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
