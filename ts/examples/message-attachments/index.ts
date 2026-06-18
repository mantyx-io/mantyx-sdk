import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  MantyxClient,
  inputFileAttachment,
  type ConversationMessage,
  type UserMessageEvent,
  type RunEvent,
} from "@mantyx/sdk";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

const samplePath = path.join(path.dirname(fileURLToPath(import.meta.url)), "sample.txt");

function loadSampleAttachment() {
  const data = fs.readFileSync(samplePath).toString("base64");
  return inputFileAttachment({
    mimeType: "text/plain",
    filename: path.basename(samplePath),
    data,
  });
}

async function runOneShot(): Promise<void> {
  console.log("=== one-shot: prompt + attachments ===");
  const attachment = loadSampleAttachment();
  const result = await client.runAgent({
    systemPrompt: "You summarize uploaded text files in two short bullets.",
    prompt: "What are the action items in the attached notes?",
    attachments: [attachment],
    onAssistantDelta: (s) => process.stdout.write(s),
  });
  process.stdout.write("\n---\n");
  console.log(result.text);
}

async function runMessages(): Promise<void> {
  console.log("=== one-shot: explicit messages array ===");
  const attachment = loadSampleAttachment();
  const messages: ConversationMessage[] = [
    { role: "system", content: "You summarize uploaded text files in two short bullets." },
    { role: "user", content: "Earlier: we discussed Q2 planning." },
    { role: "assistant", content: "Got it — send the file when you're ready." },
    {
      role: "user",
      content: "What's in the attached notes?",
      attachments: [attachment],
    },
  ];
  const result = await client.runAgent({
    messages,
    onAssistantDelta: (s) => process.stdout.write(s),
  });
  process.stdout.write("\n---\n");
  console.log(result.text);
}

async function runSession(): Promise<void> {
  console.log("=== session: send with attachments ===");
  const attachment = loadSampleAttachment();
  const session = await client.createSession({
    systemPrompt: "You summarize uploaded text files in two short bullets.",
  });
  try {
    const result = await session.send("Summarize the attached planning notes.", {
      attachments: [attachment],
      onAssistantDelta: (s) => process.stdout.write(s),
    });
    process.stdout.write("\n---\n");
    console.log(result.text);

    const events = await client.getSessionEvents(session.id, { lastMessages: 2 });
    for (const event of events) {
      if (isUserMessageEvent(event) && event.attachments?.length) {
        console.log("Replay metadata:", event.attachments);
      }
    }
  } finally {
    await session.end();
  }
}

async function main(): Promise<void> {
  const mode = process.argv[2] ?? "one-shot";
  switch (mode) {
    case "one-shot":
      await runOneShot();
      break;
    case "messages":
      await runMessages();
      break;
    case "session":
      await runSession();
      break;
    default:
      console.error(`Unknown mode ${JSON.stringify(mode)}; use one-shot, messages, or session`);
      process.exit(1);
  }
}

function isUserMessageEvent(event: RunEvent): event is UserMessageEvent {
  return event.type === "user_message";
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
