/**
 * a2a-expose — wrap a MANTYX agent as an Agent2Agent peer.
 *
 * Spins up an A2A server on http://localhost:4000 backed by a MANTYX agent.
 * Other agents can discover it at http://localhost:4000/.well-known/agent-card.json
 * and call `message/send` over JSON-RPC at the root path.
 *
 * Run:
 *   export MANTYX_API_KEY=mtx_live_...
 *   export MANTYX_WORKSPACE_SLUG=acme-corp
 *   # Either point at a persisted agent...
 *   export MANTYX_AGENT_ID=agent_cm6abc123
 *   # ...or rely on the default ephemeral system prompt below.
 *   npm install            # pulls @a2a-js/sdk + express
 *   npm start
 *
 * Probe it from another shell:
 *   curl http://localhost:4000/.well-known/agent-card.json | jq .
 *   curl -X POST http://localhost:4000 -H "content-type: application/json" \
 *     -d '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","messageId":"u1","role":"user","parts":[{"kind":"text","text":"Hi!"}]}}}' | jq .
 */
import { MantyxClient } from "@mantyx/sdk";
import { serveAgentOverA2A } from "@mantyx/sdk/a2a-server";
import type { AgentCard } from "@a2a-js/sdk";

const apiKey = requireEnv("MANTYX_API_KEY");
const workspaceSlug = requireEnv("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({ apiKey, workspaceSlug });

const port = Number(process.env.PORT ?? 4000);
const publicUrl = process.env.PUBLIC_URL ?? `http://localhost:${port}`;

const agentCard: AgentCard = {
  name: process.env.AGENT_NAME ?? "MANTYX Demo Agent",
  description:
    process.env.AGENT_DESCRIPTION ??
    "A MANTYX agent exposed as an Agent2Agent peer.",
  protocolVersion: "0.3.0",
  version: "1.0.0",
  url: publicUrl,
  skills: [
    {
      id: "chat",
      name: "Chat",
      description: "Free-form chat with the MANTYX-backed agent.",
      tags: ["chat"],
    },
  ],
  capabilities: { streaming: true, pushNotifications: false },
  defaultInputModes: ["text"],
  defaultOutputModes: ["text"],
  additionalInterfaces: [
    { url: publicUrl, transport: "JSONRPC" },
    { url: `${publicUrl}/v1`, transport: "HTTP+JSON" },
  ],
};

const agentId = process.env.MANTYX_AGENT_ID;
const handle = await serveAgentOverA2A({
  client,
  port,
  agentCard,
  agent: agentId
    ? { agentId }
    : {
        systemPrompt:
          process.env.SYSTEM_PROMPT ??
          "You are a friendly MANTYX assistant. Keep replies concise.",
        modelId: process.env.MODEL_ID,
      },
});

console.log(`MANTYX agent live at ${handle.url}`);
console.log(`Agent Card:    ${handle.url}/.well-known/agent-card.json`);
console.log(`JSON-RPC:      ${handle.url}/`);
console.log(`HTTP+JSON:     ${handle.url}/v1`);

const shutdown = async (sig: string) => {
  console.log(`\nReceived ${sig}, shutting down…`);
  await handle.close();
  process.exit(0);
};
process.on("SIGINT", () => void shutdown("SIGINT"));
process.on("SIGTERM", () => void shutdown("SIGTERM"));

function requireEnv(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`Missing required env var: ${name}`);
    process.exit(1);
  }
  return v;
}
