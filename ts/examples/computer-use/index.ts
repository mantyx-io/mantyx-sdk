import { createInterface } from "node:readline/promises";
import { chromium, type Browser } from "playwright";
import { MantyxClient, runComputerUse } from "@mantyx/sdk";
import { PlaywrightController } from "@mantyx/sdk/playwright";

const apiKey = required("MANTYX_API_KEY");
const workspaceSlug = required("MANTYX_WORKSPACE_SLUG");

// A vision-capable model in your workspace. Override via MANTYX_MODEL.
const model = process.env.MANTYX_MODEL ?? "google/gemini-3-flash";

const SCREEN_WIDTH = 1440;
const SCREEN_HEIGHT = 900;

const client = new MantyxClient({
  apiKey,
  workspaceSlug,
  ...(process.env.MANTYX_BASE_URL ? { baseUrl: process.env.MANTYX_BASE_URL } : {}),
});

async function main(): Promise<void> {
  const goal =
    process.argv.slice(2).join(" ").trim() ||
    "Go to https://news.ycombinator.com and tell me the title of the top story.";

  let browser: Browser | undefined;
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    browser = await chromium.launch({ headless: false });
    const context = await browser.newContext({
      viewport: { width: SCREEN_WIDTH, height: SCREEN_HEIGHT },
    });
    const page = await context.newPage();
    await page.goto("https://www.google.com");

    const controller = new PlaywrightController(page, {
      width: SCREEN_WIDTH,
      height: SCREEN_HEIGHT,
    });

    const out = await runComputerUse({
      client,
      model,
      goal,
      controller,
      maxSteps: 25,
      onAssistantDelta: (s: string) => process.stdout.write(s),
      onStep: (step) => {
        const names = step.actions.map((a) => a.name).join(", ") || "(final answer)";
        process.stdout.write(`\n[step ${step.step}] ${names}\n`);
      },
      // Human-in-the-loop gate: the model calls request_confirmation before
      // consequential actions and we ask on the terminal.
      confirm: async (reason: string) => {
        const answer = await rl.question(`\nConfirm? ${reason}\n[y/N] `);
        return /^y(es)?$/i.test(answer.trim());
      },
    });

    process.stdout.write("\n---\n");
    console.log(`Stopped: ${out.stoppedReason} after ${out.steps.length} step(s)`);
    console.log("Final answer:", out.finalText);
  } finally {
    rl.close();
    await browser?.close();
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
