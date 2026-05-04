import * as readline from "node:readline/promises";
import { stdin, stdout } from "node:process";
import { defineLocalTool, MantyxClient, mantyxPluginTool, mantyxTool } from "@mantyx/sdk";
import { z } from "zod";

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
  const session = await client.createSession({
    name: "repl",
    systemPrompt: "You are a friendly chat assistant. Keep replies concise.",
    tools: [randomOddTool, mantyxPluginTool("@zendesk/list-tickets")],
    // Tag every run in this session so they can be filtered in the MANTYX
    // dashboard (Agent runs → Sessions, "metadata" filter).
    metadata: {
      example: "session-chat",
      userId: "123",
      env: process.env.NODE_ENV ?? "development",
    },
  });
  console.log(`Session created (${session.id}). Type messages, Ctrl+D to exit.`);

  const rl = readline.createInterface({ input: stdin, output: stdout });
  try {
    while (true) {
      const line = await rl.question("> ");
      if (!line.trim()) continue;
      try {
        const result = await session.send(line, {
          onAssistantDelta: (s) => process.stdout.write(s),
        });
        if (result.text) process.stdout.write("\n");
      } catch (err) {
        console.error("\n[run failed]", (err as Error).message);
      }
    }
  } catch {
    // EOF
  } finally {
    rl.close();
    await session.end().catch(() => {});
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
