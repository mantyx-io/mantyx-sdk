import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  buildComputerUseSystemPrompt,
  defineComputerUseTools,
  denormalizeCoordinate,
  extractActions,
  MantyxClient,
  runComputerUse,
  type BrowserController,
  type Screenshot,
  type ScrollDirection,
  type TypeTextOptions,
} from "../src/index.js";
import type { LocalTool } from "../src/tools.js";
import type { RunResult } from "../src/client.js";
import { MockServer } from "./helpers/mock-server.js";

let server: MockServer;
let client: MantyxClient;

beforeEach(async () => {
  server = new MockServer();
  await server.start();
  client = new MantyxClient({
    apiKey: "test-key",
    workspaceSlug: "demo",
    baseUrl: server.baseUrl(),
  });
});

afterEach(async () => {
  await server.stop();
});

/** Records every controller call so tests can assert on denormalized coords. */
class RecordingController implements BrowserController {
  calls: Array<{ method: string; args: unknown[] }> = [];
  shots = 0;

  constructor(private readonly vp = { width: 1440, height: 900 }) {}

  private record(method: string, ...args: unknown[]): void {
    this.calls.push({ method, args });
  }

  viewport() {
    return this.vp;
  }
  screenshot(): Screenshot {
    this.shots += 1;
    return { data: "AAAA", mimeType: "image/jpeg" };
  }
  currentUrl(): string {
    return "https://example.com/";
  }
  navigate(url: string): void {
    this.record("navigate", url);
  }
  clickAt(x: number, y: number): void {
    this.record("clickAt", x, y);
  }
  hoverAt(x: number, y: number): void {
    this.record("hoverAt", x, y);
  }
  typeTextAt(x: number, y: number, text: string, opts: TypeTextOptions): void {
    this.record("typeTextAt", x, y, text, opts);
  }
  keyCombination(keys: string): void {
    this.record("keyCombination", keys);
  }
  scrollAt(x: number, y: number, direction: ScrollDirection, magnitude: number): void {
    this.record("scrollAt", x, y, direction, magnitude);
  }
  scrollDocument(direction: ScrollDirection): void {
    this.record("scrollDocument", direction);
  }
  dragAndDrop(x: number, y: number, dx: number, dy: number): void {
    this.record("dragAndDrop", x, y, dx, dy);
  }
  goBack(): void {
    this.record("goBack");
  }
  goForward(): void {
    this.record("goForward");
  }
}

function byName(tools: LocalTool[], name: string): LocalTool {
  const t = tools.find((x) => x.name === name);
  if (!t) throw new Error(`tool ${name} not found`);
  return t;
}

async function call(tool: LocalTool, args: Record<string, unknown>): Promise<string> {
  return (await tool.execute(args)) as string;
}

describe("denormalizeCoordinate", () => {
  it("maps the 0-1000 grid onto pixel dimensions", () => {
    expect(denormalizeCoordinate(0, 1440)).toBe(0);
    expect(denormalizeCoordinate(500, 1440)).toBe(720);
    expect(denormalizeCoordinate(500, 900)).toBe(450);
    expect(denormalizeCoordinate(1000, 1440)).toBe(1440);
  });
});

describe("defineComputerUseTools", () => {
  it("exposes the full action vocabulary plus request_confirmation by default", () => {
    const tools = defineComputerUseTools(new RecordingController());
    const names = tools.map((t) => t.name).sort();
    expect(names).toEqual(
      [
        "click_at",
        "drag_and_drop",
        "go_back",
        "go_forward",
        "hover_at",
        "key_combination",
        "navigate",
        "request_confirmation",
        "scroll_at",
        "scroll_document",
        "type_text_at",
        "wait",
      ].sort(),
    );
    for (const t of tools) expect(t.kind).toBe("local");
  });

  it("honors excludeActions and can drop request_confirmation", () => {
    const tools = defineComputerUseTools(new RecordingController(), {
      excludeActions: ["drag_and_drop", "go_back", "go_forward"],
      requestConfirmation: false,
    });
    const names = tools.map((t) => t.name);
    expect(names).not.toContain("drag_and_drop");
    expect(names).not.toContain("go_back");
    expect(names).not.toContain("go_forward");
    expect(names).not.toContain("request_confirmation");
  });

  it("denormalizes coordinates before calling the controller", async () => {
    const controller = new RecordingController({ width: 1440, height: 900 });
    const tools = defineComputerUseTools(controller);

    const out = await call(byName(tools, "click_at"), { x: 500, y: 500 });
    expect(controller.calls).toEqual([{ method: "clickAt", args: [720, 450] }]);
    // ack is JSON with status ok and the current URL.
    const parsed = JSON.parse(out) as { status: string; url?: string };
    expect(parsed.status).toBe("ok");
    expect(parsed.url).toBe("https://example.com/");
  });

  it("applies type_text_at defaults (clear + press enter)", async () => {
    const controller = new RecordingController({ width: 1000, height: 1000 });
    const tools = defineComputerUseTools(controller);

    await call(byName(tools, "type_text_at"), { x: 100, y: 200, text: "hello" });
    expect(controller.calls).toEqual([
      {
        method: "typeTextAt",
        args: [100, 200, "hello", { pressEnter: true, clearBeforeTyping: true }],
      },
    ]);

    controller.calls = [];
    await call(byName(tools, "type_text_at"), {
      x: 100,
      y: 200,
      text: "hi",
      press_enter: false,
      clear_before_typing: false,
    });
    expect(controller.calls[0]?.args[3]).toEqual({ pressEnter: false, clearBeforeTyping: false });
  });

  it("routes request_confirmation through the confirm callback", async () => {
    const reasons: string[] = [];
    const tools = defineComputerUseTools(new RecordingController(), {
      confirm: async (reason) => {
        reasons.push(reason);
        return reason.includes("safe");
      },
    });
    const tool = byName(tools, "request_confirmation");

    const approved = JSON.parse(await call(tool, { reason: "this is safe" })) as {
      approved: boolean;
    };
    const denied = JSON.parse(await call(tool, { reason: "risky purchase" })) as {
      approved: boolean;
    };

    expect(approved.approved).toBe(true);
    expect(denied.approved).toBe(false);
    expect(reasons).toEqual(["this is safe", "risky purchase"]);
  });

  it("returns a structured error for unsupported actions", async () => {
    // Minimal controller missing the optional drag handler.
    const controller: BrowserController = {
      viewport: () => ({ width: 1000, height: 1000 }),
      screenshot: () => ({ data: "x", mimeType: "image/png" }),
      clickAt: () => {},
      typeTextAt: () => {},
    };
    const tools = defineComputerUseTools(controller);
    const out = JSON.parse(
      await call(byName(tools, "drag_and_drop"), { x: 1, y: 1, destination_x: 2, destination_y: 2 }),
    ) as { status: string };
    expect(out.status).toBe("error");
  });
});

describe("buildComputerUseSystemPrompt", () => {
  it("includes the protocol and (by default) safety rules", () => {
    const prompt = buildComputerUseSystemPrompt();
    expect(prompt).toContain("0-1000 grid");
    expect(prompt).toContain("EXACTLY ONE action");
    expect(prompt).toContain("request_confirmation");
  });

  it("omits safety rules when confirmation is disabled", () => {
    const prompt = buildComputerUseSystemPrompt({ requestConfirmation: false });
    expect(prompt).not.toContain("request_confirmation");
  });
});

describe("extractActions", () => {
  it("reads local_tool_call events from a run result", () => {
    const result = {
      runId: "r",
      text: "",
      events: [
        { seq: 1, type: "assistant_delta", text: "thinking" },
        { seq: 2, type: "local_tool_call", toolUseId: "t1", name: "click_at", args: { x: 1, y: 2 } },
      ],
    } as unknown as RunResult;
    expect(extractActions(result)).toEqual([{ name: "click_at", input: { x: 1, y: 2 } }]);
  });
});

describe("runComputerUse loop", () => {
  it("screenshots each turn, runs actions, and stops on a final answer", async () => {
    const controller = new RecordingController({ width: 1440, height: 900 });

    // Turn 1: the model clicks. Turn 2: it answers in plain text (no tool).
    server.scriptForNextSessionRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "tu_1",
          name: "click_at",
          args: { x: 500, y: 500 },
          awaitToolResult: true,
        },
        { type: "result", subtype: "success", text: "" },
      ],
    };

    const sentAttachments: number[] = [];

    const out = await runComputerUse({
      client,
      goal: "Find the top story.",
      controller,
      maxSteps: 5,
      onStep: (step) => {
        // Capture how many attachments the turn carried.
        const body = server.lastSessionMessageBody as
          | { messages?: Array<{ attachments?: unknown[] }> }
          | null;
        sentAttachments.push(body?.messages?.[0]?.attachments?.length ?? 0);
        // Arm the next turn: a plain-text final answer ends the loop.
        if (step.step === 1) {
          server.scriptForNextSessionRun = {
            events: [{ type: "result", subtype: "success", text: "The top story is X." }],
          };
        }
      },
    });

    expect(out.stoppedReason).toBe("completed");
    expect(out.finalText).toBe("The top story is X.");
    expect(out.steps).toHaveLength(2);

    // One screenshot per step.
    expect(controller.shots).toBe(2);
    // The click was denormalized (500/1000 * {1440,900}) and executed.
    expect(controller.calls).toEqual([{ method: "clickAt", args: [720, 450] }]);

    // Step 1 took an action; step 2 produced the final answer.
    expect(out.steps[0]?.actions).toEqual([{ name: "click_at", input: { x: 500, y: 500 } }]);
    expect(out.steps[1]?.actions).toEqual([]);

    // Each turn shipped exactly one screenshot attachment.
    expect(sentAttachments).toEqual([1, 1]);
  });

  it("stops at maxSteps when the model keeps acting", async () => {
    const controller = new RecordingController();

    const armClick = (): void => {
      server.scriptForNextSessionRun = {
        events: [
          {
            type: "local_tool_call",
            toolUseId: `tu_${Math.random()}`,
            name: "scroll_document",
            args: { direction: "down" },
            awaitToolResult: true,
          },
          { type: "result", subtype: "success", text: "scrolling" },
        ],
      };
    };

    armClick();

    const out = await runComputerUse({
      client,
      goal: "Scroll forever.",
      controller,
      maxSteps: 3,
      onStep: () => armClick(),
    });

    expect(out.stoppedReason).toBe("max_steps");
    expect(out.steps).toHaveLength(3);
    expect(controller.shots).toBe(3);
  });
});
