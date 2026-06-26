/**
 * Computer use — an SDK-level browser-control loop built on the existing
 * MANTYX primitives (local tools + sessions + file attachments).
 *
 * Unlike a provider's dedicated "computer use" model, this implementation is
 * **model-agnostic**: it works with any vision-capable model in your
 * workspace. The trade-off is that a general model grounds pixel coordinates
 * less precisely than a model tuned for GUI control; the upside is you keep
 * full access to everything the wire protocol offers (loop detection, tool
 * budgets, the supervisor judge, observability) and you are not locked to one
 * vendor.
 *
 * How it works:
 *
 *   1. The action vocabulary (`click_at`, `type_text_at`, `scroll_at`, …) is
 *      exposed as ordinary {@link defineLocalTool | local tools}. The model
 *      emits a `local_tool_call`, the SDK runs it against your
 *      {@link BrowserController}, and posts a short text ack back.
 *   2. Screenshots flow the other direction. Tool results are string-only on
 *      the wire, so each step the loop takes a fresh screenshot and sends it
 *      as a **user-message attachment** on the next turn (see
 *      `docs/agent-runs-protocol.md` §4.0.1). That is what the model "sees".
 *   3. {@link runComputerUse} owns the outer loop: screenshot → send → let the
 *      model take one action → repeat, until the model answers with plain
 *      text (no tool call) or `maxSteps` is hit.
 *
 * Coordinates from the model are on a normalized 0-1000 grid (resolution
 * independent). Each action tool denormalizes against
 * {@link BrowserController.viewport} before calling the controller, so your
 * controller always works in real pixels.
 */
import { z } from "zod";
import { inputFileAttachment } from "./client.js";
import type { AgentSession, MantyxClient, RunEvent, RunResult } from "./client.js";
import { defineLocalTool, type LocalTool, type ReasoningLevel } from "./tools.js";

// --------------------------------------------------------------- Controller

/** Scroll direction shared by `scroll_at` / `scroll_document`. */
export type ScrollDirection = "up" | "down" | "left" | "right";

/** Pixel viewport of the controlled surface, used to denormalize coordinates. */
export interface Viewport {
  width: number;
  height: number;
}

/** A captured screenshot, base64-encoded (no `data:` prefix). */
export interface Screenshot {
  /** Base64-encoded image bytes (no `data:` URL prefix). */
  data: string;
  /** Image MIME type, e.g. `"image/jpeg"` or `"image/png"`. */
  mimeType: string;
  /** Optional filename for the attachment; a default is generated otherwise. */
  filename?: string;
}

/** Options forwarded to {@link BrowserController.typeTextAt}. */
export interface TypeTextOptions {
  /** Press Enter after typing. */
  pressEnter: boolean;
  /** Clear the field before typing. */
  clearBeforeTyping: boolean;
}

/**
 * The execution surface the computer-use tools drive. Coordinates passed to
 * these methods are **already denormalized to pixels** against
 * {@link viewport}. Implement this with Playwright, Puppeteer, a remote
 * browser, a mobile driver, etc. For Playwright, use
 * {@link PlaywrightController} from `@mantyx/sdk/playwright`.
 *
 * Every method may be sync or async. Methods for actions you exclude via
 * {@link DefineComputerUseToolsOptions.excludeActions} are never called and
 * can be omitted.
 */
export interface BrowserController {
  /** Current pixel viewport. Used to denormalize the model's 0-1000 coords. */
  viewport(): Viewport | Promise<Viewport>;
  /** Capture the current screen. Called once per loop step. */
  screenshot(): Screenshot | Promise<Screenshot>;
  /** Current page URL, if meaningful for the environment. Optional. */
  currentUrl?(): string | Promise<string>;
  /** Navigate directly to a URL. */
  navigate?(url: string): void | Promise<void>;
  /** Click at a pixel coordinate. */
  clickAt(x: number, y: number): void | Promise<void>;
  /** Hover at a pixel coordinate. */
  hoverAt?(x: number, y: number): void | Promise<void>;
  /** Type `text` at a pixel coordinate. */
  typeTextAt(x: number, y: number, text: string, opts: TypeTextOptions): void | Promise<void>;
  /** Press a key or chord, e.g. `"Enter"` or `"Control+C"`. */
  keyCombination?(keys: string): void | Promise<void>;
  /** Scroll the element at a pixel coordinate by `magnitude` pixels. */
  scrollAt?(
    x: number,
    y: number,
    direction: ScrollDirection,
    magnitude: number,
  ): void | Promise<void>;
  /** Scroll the whole document. */
  scrollDocument?(direction: ScrollDirection): void | Promise<void>;
  /** Drag from one pixel coordinate to another. */
  dragAndDrop?(x: number, y: number, destinationX: number, destinationY: number): void | Promise<void>;
  /** Navigate back in history. */
  goBack?(): void | Promise<void>;
  /** Navigate forward in history. */
  goForward?(): void | Promise<void>;
}

// --------------------------------------------------------------- Coordinates

/**
 * Convert a normalized coordinate (0-1000 grid, matching the convention used
 * by GUI-control models) to an actual pixel value.
 */
export function denormalizeCoordinate(value: number, size: number): number {
  return Math.round((value / 1000) * size);
}

// --------------------------------------------------------------- Tool factory

/** The canonical computer-use action tool names. */
export const COMPUTER_USE_ACTIONS = [
  "navigate",
  "click_at",
  "hover_at",
  "type_text_at",
  "key_combination",
  "scroll_at",
  "scroll_document",
  "drag_and_drop",
  "go_back",
  "go_forward",
  "wait",
] as const;

export type ComputerUseAction = (typeof COMPUTER_USE_ACTIONS)[number];

export interface DefineComputerUseToolsOptions {
  /**
   * Predefined actions to omit (mirrors a dedicated model's
   * `excluded_predefined_functions`). Useful when an environment cannot
   * support an action, e.g. excluding `drag_and_drop` or the navigation
   * actions for a kiosk.
   */
  excludeActions?: ComputerUseAction[];
  /**
   * Include a `request_confirmation` tool the model is told to call before
   * consequential or irreversible actions. The returned promise/boolean from
   * `confirm` decides whether the model is cleared to proceed. Defaults to
   * `true`; when no `confirm` is supplied, confirmation requests are denied.
   */
  requestConfirmation?: boolean;
  /**
   * Human-in-the-loop gate invoked by the `request_confirmation` tool. Return
   * `true` to allow the model to continue with the action it described.
   */
  confirm?: (reason: string) => boolean | Promise<boolean>;
}

const WAIT_HINT =
  "Action performed. A fresh screenshot will be provided in the next message; do not call another action until you have seen it.";

/**
 * Build the set of computer-use action tools bound to a
 * {@link BrowserController}. Pass the result as `tools` on a session / run, or
 * let {@link runComputerUse} wire everything for you.
 */
export function defineComputerUseTools(
  controller: BrowserController,
  opts: DefineComputerUseToolsOptions = {},
): LocalTool[] {
  const excluded = new Set<ComputerUseAction>(opts.excludeActions ?? []);
  const includeConfirm = opts.requestConfirmation ?? true;

  const px = async (x: number, y: number): Promise<{ x: number; y: number }> => {
    const vp = await controller.viewport();
    return { x: denormalizeCoordinate(x, vp.width), y: denormalizeCoordinate(y, vp.height) };
  };

  const ack = async (note?: string): Promise<string> => {
    let url: string | undefined;
    if (controller.currentUrl) {
      try {
        url = await controller.currentUrl();
      } catch {
        // currentUrl is best-effort; ignore failures.
      }
    }
    return JSON.stringify({
      status: "ok",
      ...(note ? { note } : {}),
      ...(url ? { url } : {}),
      hint: WAIT_HINT,
    });
  };

  const tools: LocalTool[] = [];
  const add = (action: ComputerUseAction, build: () => LocalTool): void => {
    if (!excluded.has(action)) tools.push(build());
  };

  add("navigate", () =>
    defineLocalTool({
      name: "navigate",
      description: "Navigate the browser directly to a URL.",
      parameters: z.object({ url: z.string().describe("Absolute URL to open.") }),
      execute: async ({ url }) => {
        if (!controller.navigate) return errorResult("navigate is not supported by this environment");
        await controller.navigate(url);
        return ack();
      },
    }),
  );

  add("click_at", () =>
    defineLocalTool({
      name: "click_at",
      description:
        "Click at a coordinate. x and y are on a 0-1000 grid scaled to the screen.",
      parameters: z.object({
        x: z.number().describe("Horizontal coordinate, 0-1000."),
        y: z.number().describe("Vertical coordinate, 0-1000."),
      }),
      execute: async ({ x, y }) => {
        const p = await px(x, y);
        await controller.clickAt(p.x, p.y);
        return ack();
      },
    }),
  );

  add("hover_at", () =>
    defineLocalTool({
      name: "hover_at",
      description:
        "Hover the mouse at a coordinate (useful to reveal menus). x and y are on a 0-1000 grid.",
      parameters: z.object({
        x: z.number().describe("Horizontal coordinate, 0-1000."),
        y: z.number().describe("Vertical coordinate, 0-1000."),
      }),
      execute: async ({ x, y }) => {
        if (!controller.hoverAt) return errorResult("hover_at is not supported by this environment");
        const p = await px(x, y);
        await controller.hoverAt(p.x, p.y);
        return ack();
      },
    }),
  );

  add("type_text_at", () =>
    defineLocalTool({
      name: "type_text_at",
      description:
        "Type text at a coordinate. By default clears the field first and presses Enter after typing. x and y are on a 0-1000 grid.",
      parameters: z.object({
        x: z.number().describe("Horizontal coordinate, 0-1000."),
        y: z.number().describe("Vertical coordinate, 0-1000."),
        text: z.string().describe("Text to type."),
        press_enter: z.boolean().optional().describe("Press Enter after typing. Default true."),
        clear_before_typing: z
          .boolean()
          .optional()
          .describe("Clear the field before typing. Default true."),
      }),
      execute: async ({ x, y, text, press_enter, clear_before_typing }) => {
        const p = await px(x, y);
        await controller.typeTextAt(p.x, p.y, text, {
          pressEnter: press_enter ?? true,
          clearBeforeTyping: clear_before_typing ?? true,
        });
        return ack();
      },
    }),
  );

  add("key_combination", () =>
    defineLocalTool({
      name: "key_combination",
      description:
        'Press a key or chord, e.g. "Enter", "Control+C", "Control+A". Useful for submitting or clipboard actions.',
      parameters: z.object({
        keys: z.string().describe('Key or chord, e.g. "Enter" or "Control+C".'),
      }),
      execute: async ({ keys }) => {
        if (!controller.keyCombination)
          return errorResult("key_combination is not supported by this environment");
        await controller.keyCombination(keys);
        return ack();
      },
    }),
  );

  add("scroll_at", () =>
    defineLocalTool({
      name: "scroll_at",
      description:
        "Scroll the area at a coordinate in a direction by a pixel magnitude (default 800). Coordinates are on a 0-1000 grid.",
      parameters: z.object({
        x: z.number().describe("Horizontal coordinate, 0-1000."),
        y: z.number().describe("Vertical coordinate, 0-1000."),
        direction: z.enum(["up", "down", "left", "right"]),
        magnitude: z.number().optional().describe("Scroll distance in pixels. Default 800."),
      }),
      execute: async ({ x, y, direction, magnitude }) => {
        if (!controller.scrollAt) return errorResult("scroll_at is not supported by this environment");
        const p = await px(x, y);
        await controller.scrollAt(p.x, p.y, direction, magnitude ?? 800);
        return ack();
      },
    }),
  );

  add("scroll_document", () =>
    defineLocalTool({
      name: "scroll_document",
      description: 'Scroll the whole page "up", "down", "left", or "right".',
      parameters: z.object({ direction: z.enum(["up", "down", "left", "right"]) }),
      execute: async ({ direction }) => {
        if (!controller.scrollDocument)
          return errorResult("scroll_document is not supported by this environment");
        await controller.scrollDocument(direction);
        return ack();
      },
    }),
  );

  add("drag_and_drop", () =>
    defineLocalTool({
      name: "drag_and_drop",
      description:
        "Drag from a starting coordinate and drop at a destination coordinate. All coordinates are on a 0-1000 grid.",
      parameters: z.object({
        x: z.number().describe("Start horizontal, 0-1000."),
        y: z.number().describe("Start vertical, 0-1000."),
        destination_x: z.number().describe("Destination horizontal, 0-1000."),
        destination_y: z.number().describe("Destination vertical, 0-1000."),
      }),
      execute: async ({ x, y, destination_x, destination_y }) => {
        if (!controller.dragAndDrop)
          return errorResult("drag_and_drop is not supported by this environment");
        const from = await px(x, y);
        const to = await px(destination_x, destination_y);
        await controller.dragAndDrop(from.x, from.y, to.x, to.y);
        return ack();
      },
    }),
  );

  add("go_back", () =>
    defineLocalTool({
      name: "go_back",
      description: "Navigate to the previous page in history.",
      parameters: z.object({}),
      execute: async () => {
        if (!controller.goBack) return errorResult("go_back is not supported by this environment");
        await controller.goBack();
        return ack();
      },
    }),
  );

  add("go_forward", () =>
    defineLocalTool({
      name: "go_forward",
      description: "Navigate to the next page in history.",
      parameters: z.object({}),
      execute: async () => {
        if (!controller.goForward)
          return errorResult("go_forward is not supported by this environment");
        await controller.goForward();
        return ack();
      },
    }),
  );

  add("wait", () =>
    defineLocalTool({
      name: "wait",
      description: "Pause to let dynamic content load or animations finish.",
      parameters: z.object({
        seconds: z.number().optional().describe("Seconds to wait. Default 1, max 10."),
      }),
      execute: async ({ seconds }) => {
        const s = Math.min(Math.max(seconds ?? 1, 0), 10);
        await new Promise((resolve) => setTimeout(resolve, s * 1000));
        return ack(`waited ${s}s`);
      },
    }),
  );

  if (includeConfirm) {
    tools.push(
      defineLocalTool({
        name: "request_confirmation",
        description:
          "Ask the human operator to approve a consequential or irreversible action BEFORE performing it (e.g. submitting a form, sending a message, making a purchase, accepting terms, solving a CAPTCHA, logging in). Returns whether you are cleared to proceed.",
        parameters: z.object({
          reason: z.string().describe("What you are about to do and why confirmation is needed."),
        }),
        execute: async ({ reason }) => {
          const approved = opts.confirm ? await opts.confirm(reason) : false;
          return JSON.stringify({
            approved,
            ...(approved
              ? { note: "Operator approved. Proceed with the action, then stop and wait for the next screenshot." }
              : { note: "Operator denied. Do not perform the action; choose a different approach or stop and report back." }),
          });
        },
      }),
    );
  }

  return tools;
}

function errorResult(message: string): string {
  return JSON.stringify({ status: "error", error: message });
}

// --------------------------------------------------------------- System prompt

export interface ComputerUseSystemPromptOptions {
  /** Environment label injected into the prompt, e.g. `"web browser"`. */
  environment?: string;
  /** Extra task-specific context appended to the standard instructions. */
  extraInstructions?: string;
  /** Whether the `request_confirmation` tool is available (adds safety rules). */
  requestConfirmation?: boolean;
}

/**
 * Build the system prompt that drives the loop: it teaches the model the
 * screenshot-per-step protocol, the 0-1000 coordinate grid, the one-action-per-turn
 * discipline, when to stop, and the human-in-the-loop safety rules.
 */
export function buildComputerUseSystemPrompt(
  opts: ComputerUseSystemPromptOptions = {},
): string {
  const environment = opts.environment ?? "web browser";
  const confirmation = opts.requestConfirmation ?? true;

  const lines: string[] = [
    `You are an agent that controls a ${environment} by looking at screenshots and taking UI actions.`,
    "",
    "Protocol:",
    `- Before every step you receive a screenshot of the current ${environment} as an image attachment. Study it carefully.`,
    "- Choose EXACTLY ONE action and call the matching tool. Coordinates use a 0-1000 grid (top-left is 0,0; bottom-right is 1000,1000), scaled automatically to the screen.",
    "- After the tool runs, STOP and wait. You will be given a new screenshot reflecting the result; only then decide the next action.",
    "- Do not chain multiple actions in one turn and do not guess what happened — always rely on the next screenshot.",
    "- Scroll to reveal content before concluding something is missing.",
    "",
    "Finishing:",
    "- When the task is complete (or you have gathered enough information to answer), reply with a plain-text final answer and DO NOT call any tool. That ends the session.",
    "- If you get stuck or cannot proceed, explain why in plain text without calling a tool.",
  ];

  if (confirmation) {
    lines.push(
      "",
      "Safety — you MUST call `request_confirmation` and wait for approval BEFORE any consequential or irreversible action, including:",
      "- Accepting terms of service, privacy policies, cookie banners, or other agreements.",
      "- Solving or bypassing CAPTCHAs or other human-verification checks.",
      "- Completing a purchase, moving money, or any financial transaction.",
      "- Sending an email or message, or posting content.",
      "- Logging into an account, or accessing/modifying sensitive personal data.",
      "- Downloading, saving, sharing, or transferring files or user data.",
      "For these, do all preparatory steps first, then call `request_confirmation` immediately BEFORE the final irreversible click. If denied, do not perform the action.",
    );
  }

  if (opts.extraInstructions) {
    lines.push("", opts.extraInstructions);
  }

  return lines.join("\n");
}

// --------------------------------------------------------------- Loop driver

/** One action the model invoked on a turn (derived from `local_tool_call` events). */
export interface ComputerUseActionCall {
  name: string;
  input: Record<string, unknown>;
}

/** A single iteration of the computer-use loop. */
export interface ComputerUseStep {
  /** 1-based step index. */
  step: number;
  /** The screenshot sent to the model at the start of this step. */
  screenshot: Screenshot;
  /** The full run result for the turn. */
  result: RunResult;
  /** Actions the model invoked this turn (empty when it produced a final answer). */
  actions: ComputerUseActionCall[];
}

export interface RunComputerUseOptions {
  /** MANTYX client used to open the session. */
  client: MantyxClient;
  /** The user's goal, sent on the first turn. */
  goal: string;
  /** Drives the actual UI actions and screenshots. */
  controller: BrowserController;
  /** Vision-capable model id (`modelId`). Omit to use the workspace default. */
  model?: string;
  /** Max loop iterations before stopping. Default 20. */
  maxSteps?: number;
  /** Override the generated system prompt entirely. */
  systemPrompt?: string;
  /** Tweaks for the generated system prompt (ignored when `systemPrompt` is set). */
  systemPromptOptions?: ComputerUseSystemPromptOptions;
  /** Actions to omit from the tool surface. */
  excludeActions?: ComputerUseAction[];
  /** Human-in-the-loop gate for `request_confirmation`. Omit to disable confirmations. */
  confirm?: (reason: string) => boolean | Promise<boolean>;
  /** Disable the `request_confirmation` tool entirely. Default false (it is included). */
  disableConfirmation?: boolean;
  /** Provider reasoning strength for the session. */
  reasoningLevel?: ReasoningLevel;
  /** Observability metadata stored on the session. */
  metadata?: Record<string, string>;
  /** Text sent alongside the screenshot on every turn after the first. */
  continueText?: string;
  /** Invoked after each step (await is honored), e.g. for logging. */
  onStep?: (step: ComputerUseStep) => void | Promise<void>;
  /** Streams assistant text deltas. */
  onAssistantDelta?: (delta: string) => void;
  /** Aborts the loop and the in-flight turn. */
  signal?: AbortSignal;
}

export interface ComputerUseResult {
  /** The model's final plain-text answer (text of the last turn). */
  finalText: string;
  /** Every step taken, in order. */
  steps: ComputerUseStep[];
  /** The session that ran the loop (already ended). */
  sessionId: string;
  /** Why the loop stopped. */
  stoppedReason: "completed" | "max_steps";
}

const DEFAULT_CONTINUE_TEXT =
  "Here is the screen after your last action. Continue toward the goal, or give your final answer if the task is done.";

/** Pull the actions the model invoked this turn from the run's events. */
export function extractActions(result: RunResult): ComputerUseActionCall[] {
  const actions: ComputerUseActionCall[] = [];
  for (const ev of result.events as RunEvent[]) {
    if (ev.type === "local_tool_call") {
      const e = ev as { name?: string; args?: Record<string, unknown> };
      actions.push({ name: e.name ?? "", input: e.args ?? {} });
    }
  }
  return actions;
}

function screenshotExtension(mimeType: string): string {
  if (mimeType.includes("png")) return "png";
  if (mimeType.includes("webp")) return "webp";
  return "jpg";
}

/**
 * Run a model-agnostic computer-use loop: screenshot → send → one action →
 * repeat, until the model answers in plain text or `maxSteps` is reached. The
 * session is created internally and ended on return.
 *
 * @example
 * const out = await runComputerUse({
 *   client,
 *   model: "google/gemini-3-flash",
 *   goal: "Find the cheapest 2-door smart fridge on the site and report its price.",
 *   controller: playwrightController, // implements BrowserController
 *   confirm: async (reason) => askOperator(reason),
 * });
 * console.log(out.finalText);
 */
export async function runComputerUse(opts: RunComputerUseOptions): Promise<ComputerUseResult> {
  const maxSteps = opts.maxSteps ?? 20;
  if (maxSteps < 1) throw new Error("runComputerUse: maxSteps must be >= 1");

  const requestConfirmation = !opts.disableConfirmation;
  const tools = defineComputerUseTools(opts.controller, {
    ...(opts.excludeActions ? { excludeActions: opts.excludeActions } : {}),
    requestConfirmation,
    ...(opts.confirm ? { confirm: opts.confirm } : {}),
  });

  const systemPrompt =
    opts.systemPrompt ??
    buildComputerUseSystemPrompt({
      ...(opts.systemPromptOptions ?? {}),
      requestConfirmation,
    });

  const session: AgentSession = await opts.client.createSession({
    systemPrompt,
    ...(opts.model ? { modelId: opts.model } : {}),
    tools,
    ...(opts.reasoningLevel !== undefined ? { reasoningLevel: opts.reasoningLevel } : {}),
    ...(opts.metadata ? { metadata: opts.metadata } : {}),
  });

  const steps: ComputerUseStep[] = [];
  let finalText = "";
  let stoppedReason: "completed" | "max_steps" = "max_steps";

  try {
    for (let i = 0; i < maxSteps; i++) {
      const screenshot = await opts.controller.screenshot();
      const filename =
        screenshot.filename ?? `screenshot-${i + 1}.${screenshotExtension(screenshot.mimeType)}`;
      const attachment = inputFileAttachment({
        mimeType: screenshot.mimeType,
        filename,
        data: screenshot.data,
      });

      const turnText = i === 0 ? opts.goal : opts.continueText ?? DEFAULT_CONTINUE_TEXT;
      const result = await session.send(turnText, {
        attachments: [attachment],
        ...(opts.onAssistantDelta ? { onAssistantDelta: opts.onAssistantDelta } : {}),
        ...(opts.signal ? { signal: opts.signal } : {}),
      });

      const actions = extractActions(result);
      const step: ComputerUseStep = { step: i + 1, screenshot, result, actions };
      steps.push(step);
      finalText = result.text;

      if (opts.onStep) await opts.onStep(step);

      if (actions.length === 0) {
        stoppedReason = "completed";
        break;
      }
    }

    return { finalText, steps, sessionId: session.id, stoppedReason };
  } finally {
    await session.end().catch(() => {
      // Best-effort cleanup; surfacing an end() failure would mask the real result.
    });
  }
}
