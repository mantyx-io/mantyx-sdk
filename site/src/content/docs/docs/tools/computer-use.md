---
title: Computer use
description: A model-agnostic screenshot-and-action loop for controlling a browser or GUI.
sidebar:
  order: 6
---

Computer use lets an agent control a graphical interface — typically a browser — by **looking at screenshots** and **taking UI actions** (click, type, scroll, drag). Instead of relying on a vendor's dedicated "computer use" model, the SDK builds the loop on top of the primitives you already have: [local tools](/docs/tools/local/) for the actions and [session](/docs/agents/sessions/) message **attachments** for the screenshots.

The upside is that this works with **any vision-capable model** in your workspace and keeps everything the wire protocol offers — [run guards](/docs/run-guards/), the supervisor judge, [metadata](/docs/metadata/) and observability. The trade-off is that a general-purpose model grounds pixel coordinates less precisely than a model fine-tuned for GUI control, so expect occasional mis-clicks and keep the loop bounded.

:::note
Computer use currently ships in the **TypeScript** SDK (`@mantyx/sdk`).
:::

## How it works

Tool results on the wire are string-only, so a screenshot can't ride back on a tool result. The loop splits the two directions:

- **Actions out** — the model calls action tools (`click_at`, `type_text_at`, …); the SDK runs them against your `BrowserController` and posts a short text ack.
- **Screenshots in** — each step the SDK captures a fresh screenshot and sends it as a **user-message attachment** on the next turn. That image is what the model "sees".

```text
runComputerUse(goal, model, controller)
  │
  ├─ createSession(systemPrompt + action tools + model)
  │
  ▼
 ┌───────────────────────────────────────────────┐
 │ 1. controller.screenshot()                     │
 │ 2. session.send(text, attachments:[screenshot])│
 │ 3. model inspects screenshot, calls ONE action │
 │ 4. tool runs via controller, returns text ack  │
 └───────────────────────────────────────────────┘
   │ took an action and under maxSteps  → loop back to step 1
   │ replied with text (no tool call)   → return { finalText, steps }
```

The loop stops when the model answers in plain text (no tool call) or `maxSteps` is reached. Coordinates from the model are on a normalized **0-1000 grid**; each action tool denormalizes against the controller's viewport, so your controller always works in real pixels.

## Quickstart

Install Playwright, then use the built-in `PlaywrightController` with `runComputerUse`:

```ts
import { chromium } from "playwright";
import { MantyxClient, runComputerUse } from "@mantyx/sdk";
import { PlaywrightController } from "@mantyx/sdk/playwright";

const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
  workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
});

const browser = await chromium.launch({ headless: false });
const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
await page.goto("https://www.google.com");

const controller = new PlaywrightController(page, { width: 1440, height: 900 });

const out = await runComputerUse({
  client,
  model: "google/gemini-3-flash", // any vision-capable model id
  goal: "Search Wikipedia for the Apollo program and report the launch year of Apollo 11.",
  controller,
  maxSteps: 25,
  confirm: async (reason) => askOperator(reason), // human-in-the-loop gate
});

console.log(out.finalText);
await browser.close();
```

`PlaywrightController` lives on `@mantyx/sdk/playwright` so the main package does not pull in Playwright unless you opt in (`npm install playwright`). You can also implement `BrowserController` yourself for Puppeteer, a remote browser, or a mobile driver.

A complete runnable version lives at [`examples/computer-use`](/docs/examples/).

## The action vocabulary

`runComputerUse` (or `defineComputerUseTools`) registers these local tools. Coordinates are on the 0-1000 grid.

| Tool | Arguments | Controller method |
| ---- | --------- | ----------------- |
| `navigate` | `url` | `navigate` |
| `click_at` | `x`, `y` | `clickAt` |
| `hover_at` | `x`, `y` | `hoverAt` |
| `type_text_at` | `x`, `y`, `text`, `press_enter?`, `clear_before_typing?` | `typeTextAt` |
| `key_combination` | `keys` | `keyCombination` |
| `scroll_at` | `x`, `y`, `direction`, `magnitude?` | `scrollAt` |
| `scroll_document` | `direction` | `scrollDocument` |
| `drag_and_drop` | `x`, `y`, `destination_x`, `destination_y` | `dragAndDrop` |
| `go_back` | — | `goBack` |
| `go_forward` | — | `goForward` |
| `wait` | `seconds?` | — (handled in-SDK) |
| `request_confirmation` | `reason` | — (calls your `confirm`) |

Only `viewport`, `screenshot`, `clickAt`, and `typeTextAt` are required on the controller. Tools whose controller method is missing return a structured error to the model; you can also drop tools entirely with `excludeActions`.

## Safety

Computer use carries real risk: pages can carry prompt-injection, the model can misread the screen, and a wrong click can be irreversible. Two layers help:

- **Human-in-the-loop.** The generated system prompt instructs the model to call `request_confirmation` **before** consequential or irreversible actions — purchases, sending messages, logging in, accepting terms, solving CAPTCHAs, touching sensitive data. Your `confirm(reason)` callback decides whether to proceed; omit it and confirmation requests are denied.
- **Sandboxing.** Run automation in a dedicated browser profile, container, or VM, add a URL allow/blocklist in your controller, and log screenshots, proposed actions, and what was executed.

## API

| Symbol | Purpose |
| ------ | ------- |
| `runComputerUse(options)` | Creates a session and drives the full screenshot/action loop; returns `{ finalText, steps, sessionId, stoppedReason }`. |
| `defineComputerUseTools(controller, opts?)` | Returns the action `LocalTool[]` if you want to drive the session yourself. |
| `buildComputerUseSystemPrompt(opts?)` | The system prompt used by the loop (protocol + safety rules). |
| `BrowserController` | The interface your environment implements. |
| `PlaywrightController` | Built-in `BrowserController` for Playwright (`@mantyx/sdk/playwright`). |
| `denormalizeCoordinate(value, size)` | 0-1000 grid → pixels helper. |

### Limitations

- Coordinate grounding accuracy depends on the model; bound `maxSteps`.
- Screenshots are sent inline (base64). Keep them under the 5 MB inline attachment cap — prefer JPEG and downscale large/retina viewports.
- Cost and latency scale with steps: each step is one model turn plus one screenshot.
