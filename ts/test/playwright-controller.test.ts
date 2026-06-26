import { describe, expect, it, vi } from "vitest";
import { PlaywrightController, scrollDelta } from "../src/playwright.js";

/** Minimal Playwright Page stub for unit tests. */
function mockPage() {
  const calls: Array<{ method: string; args: unknown[] }> = [];
  const page = {
    url: () => "https://example.com/",
    screenshot: vi.fn(async (opts?: { type?: string; quality?: number }) => {
      calls.push({ method: "screenshot", args: [opts ?? {}] });
      return Buffer.from("fake-image");
    }),
    goto: vi.fn(async (url: string) => {
      calls.push({ method: "goto", args: [url] });
    }),
    goBack: vi.fn(async () => {
      calls.push({ method: "goBack", args: [] });
    }),
    goForward: vi.fn(async () => {
      calls.push({ method: "goForward", args: [] });
    }),
    waitForLoadState: vi.fn(async () => {
      calls.push({ method: "waitForLoadState", args: [] });
    }),
    mouse: {
      click: vi.fn(async (x: number, y: number) => {
        calls.push({ method: "mouse.click", args: [x, y] });
      }),
      move: vi.fn(async (x: number, y: number, opts?: { steps?: number }) => {
        calls.push({ method: "mouse.move", args: [x, y, opts] });
      }),
      down: vi.fn(async () => {
        calls.push({ method: "mouse.down", args: [] });
      }),
      up: vi.fn(async () => {
        calls.push({ method: "mouse.up", args: [] });
      }),
      wheel: vi.fn(async (dx: number, dy: number) => {
        calls.push({ method: "mouse.wheel", args: [dx, dy] });
      }),
    },
    keyboard: {
      press: vi.fn(async (key: string) => {
        calls.push({ method: "keyboard.press", args: [key] });
      }),
      type: vi.fn(async (text: string) => {
        calls.push({ method: "keyboard.type", args: [text] });
      }),
    },
  };
  return { page, calls };
}

describe("scrollDelta", () => {
  it("maps directions to wheel deltas", () => {
    expect(scrollDelta("up", 100)).toEqual([0, -100]);
    expect(scrollDelta("down", 200)).toEqual([0, 200]);
    expect(scrollDelta("left", 50)).toEqual([-50, 0]);
    expect(scrollDelta("right", 75)).toEqual([75, 0]);
  });
});

describe("PlaywrightController", () => {
  it("reports the configured viewport", () => {
    const { page } = mockPage();
    const controller = new PlaywrightController(page as never, { width: 800, height: 600 });
    expect(controller.viewport()).toEqual({ width: 800, height: 600 });
  });

  it("captures JPEG screenshots by default", async () => {
    const { page } = mockPage();
    const controller = new PlaywrightController(page as never);
    const shot = await controller.screenshot();
    expect(shot.mimeType).toBe("image/jpeg");
    expect(shot.data).toBe(Buffer.from("fake-image").toString("base64"));
    expect(page.screenshot).toHaveBeenCalledWith({ type: "jpeg", quality: 70 });
  });

  it("can capture PNG screenshots", async () => {
    const { page } = mockPage();
    const controller = new PlaywrightController(page as never, { screenshotType: "png" });
    const shot = await controller.screenshot();
    expect(shot.mimeType).toBe("image/png");
    expect(page.screenshot).toHaveBeenCalledWith({ type: "png" });
  });

  it("clicks at pixel coordinates and settles", async () => {
    const { page, calls } = mockPage();
    const controller = new PlaywrightController(page as never, { settleTimeoutMs: 0 });
    await controller.clickAt(100, 200);
    expect(calls.filter((c) => c.method === "mouse.click")).toEqual([
      { method: "mouse.click", args: [100, 200] },
    ]);
  });

  it("types text with clear and enter defaults", async () => {
    const { page, calls } = mockPage();
    const controller = new PlaywrightController(page as never, { settleTimeoutMs: 0 });
    await controller.typeTextAt(10, 20, "hello", {
      pressEnter: true,
      clearBeforeTyping: true,
    });
    expect(calls.map((c) => c.method)).toEqual([
      "mouse.click",
      "keyboard.press",
      "keyboard.press",
      "keyboard.type",
      "keyboard.press",
    ]);
  });

  it("normalizes key chords before pressing", async () => {
    const { page, calls } = mockPage();
    const controller = new PlaywrightController(page as never, { settleTimeoutMs: 0 });
    await controller.keyCombination("Control + C");
    expect(calls.find((c) => c.method === "keyboard.press")?.args).toEqual(["Control+C"]);
  });

  it("scrolls the document", async () => {
    const { page, calls } = mockPage();
    const controller = new PlaywrightController(page as never, { settleTimeoutMs: 0 });
    await controller.scrollDocument("down");
    expect(calls.find((c) => c.method === "mouse.wheel")?.args).toEqual([0, 800]);
  });

  it("returns the current URL", () => {
    const { page } = mockPage();
    const controller = new PlaywrightController(page as never);
    expect(controller.currentUrl()).toBe("https://example.com/");
  });
});
