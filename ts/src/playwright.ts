/**
 * Playwright-backed {@link BrowserController} for computer use.
 *
 * Loaded from a separate sub-export (`@mantyx/sdk/playwright`) so apps that
 * don't drive a browser never pay the Playwright bundle cost. Install the
 * optional peer dep first:
 *
 *   npm install playwright
 *   npx playwright install chromium
 *
 * @example
 *   import { MantyxClient, runComputerUse } from "@mantyx/sdk";
 *   import { PlaywrightController } from "@mantyx/sdk/playwright";
 *   import { chromium } from "playwright";
 *
 *   const browser = await chromium.launch();
 *   const page = await (await browser.newContext()).newPage();
 *   const controller = new PlaywrightController(page);
 *
 *   const out = await runComputerUse({
 *     client,
 *     goal: "Find the top story on news.ycombinator.com",
 *     controller,
 *   });
 */
import type { Page } from "playwright";
import type {
  BrowserController,
  Screenshot,
  ScrollDirection,
  TypeTextOptions,
  Viewport,
} from "./computer-use.js";

export interface PlaywrightControllerOptions {
  /**
   * Viewport width in pixels. Should match the browser context viewport so
   * denormalized coordinates from the model land on the right pixels.
   * Default `1440`.
   */
  width?: number;
  /**
   * Viewport height in pixels. Default `900`.
   */
  height?: number;
  /**
   * JPEG quality for screenshots (`0`–`100`). Ignored when
   * `screenshotType` is `"png"`. Default `70` — keeps inline attachments
   * well under the 5 MB cap.
   */
  screenshotQuality?: number;
  /** Screenshot format. Default `"jpeg"`. */
  screenshotType?: "jpeg" | "png";
  /**
   * Max milliseconds to wait for `networkidle` after navigation and UI
   * actions. Default `5000`. Set to `0` to skip waiting.
   */
  settleTimeoutMs?: number;
}

const DEFAULT_WIDTH = 1440;
const DEFAULT_HEIGHT = 900;
const DEFAULT_QUALITY = 70;
const DEFAULT_SETTLE_MS = 5000;

/**
 * A {@link BrowserController} backed by a Playwright `Page`. Coordinates
 * arrive already denormalized to pixels by the computer-use tools, so this
 * class drives Playwright directly.
 */
export class PlaywrightController implements BrowserController {
  private readonly width: number;
  private readonly height: number;
  private readonly screenshotQuality: number;
  private readonly screenshotType: "jpeg" | "png";
  private readonly settleTimeoutMs: number;

  constructor(
    private readonly page: Page,
    opts: PlaywrightControllerOptions = {},
  ) {
    this.width = opts.width ?? DEFAULT_WIDTH;
    this.height = opts.height ?? DEFAULT_HEIGHT;
    this.screenshotQuality = opts.screenshotQuality ?? DEFAULT_QUALITY;
    this.screenshotType = opts.screenshotType ?? "jpeg";
    this.settleTimeoutMs = opts.settleTimeoutMs ?? DEFAULT_SETTLE_MS;
  }

  viewport(): Viewport {
    return { width: this.width, height: this.height };
  }

  async screenshot(): Promise<Screenshot> {
    if (this.screenshotType === "png") {
      const buf = await this.page.screenshot({ type: "png" });
      return { data: buf.toString("base64"), mimeType: "image/png" };
    }
    const buf = await this.page.screenshot({
      type: "jpeg",
      quality: this.screenshotQuality,
    });
    return { data: buf.toString("base64"), mimeType: "image/jpeg" };
  }

  currentUrl(): string {
    return this.page.url();
  }

  async navigate(url: string): Promise<void> {
    await this.page.goto(url);
    await this.settle();
  }

  async clickAt(x: number, y: number): Promise<void> {
    await this.page.mouse.click(x, y);
    await this.settle();
  }

  async hoverAt(x: number, y: number): Promise<void> {
    await this.page.mouse.move(x, y);
  }

  async typeTextAt(x: number, y: number, text: string, opts: TypeTextOptions): Promise<void> {
    await this.page.mouse.click(x, y);
    if (opts.clearBeforeTyping) {
      await this.page.keyboard.press("ControlOrMeta+A");
      await this.page.keyboard.press("Backspace");
    }
    await this.page.keyboard.type(text);
    if (opts.pressEnter) await this.page.keyboard.press("Enter");
    await this.settle();
  }

  async keyCombination(keys: string): Promise<void> {
    await this.page.keyboard.press(keys.replace(/\s+/g, ""));
    await this.settle();
  }

  async scrollAt(
    x: number,
    y: number,
    direction: ScrollDirection,
    magnitude: number,
  ): Promise<void> {
    await this.page.mouse.move(x, y);
    const [dx, dy] = scrollDelta(direction, magnitude);
    await this.page.mouse.wheel(dx, dy);
    await this.settle();
  }

  async scrollDocument(direction: ScrollDirection): Promise<void> {
    const [dx, dy] = scrollDelta(direction, 800);
    await this.page.mouse.wheel(dx, dy);
    await this.settle();
  }

  async dragAndDrop(x: number, y: number, destX: number, destY: number): Promise<void> {
    await this.page.mouse.move(x, y);
    await this.page.mouse.down();
    await this.page.mouse.move(destX, destY, { steps: 10 });
    await this.page.mouse.up();
    await this.settle();
  }

  async goBack(): Promise<void> {
    await this.page.goBack();
    await this.settle();
  }

  async goForward(): Promise<void> {
    await this.page.goForward();
    await this.settle();
  }

  private async settle(): Promise<void> {
    if (this.settleTimeoutMs <= 0) return;
    try {
      await this.page.waitForLoadState("networkidle", { timeout: this.settleTimeoutMs });
    } catch {
      // Best-effort; some pages never reach networkidle.
    }
  }
}

/** @internal Exported for unit tests. */
export function scrollDelta(direction: ScrollDirection, magnitude: number): [number, number] {
  switch (direction) {
    case "up":
      return [0, -magnitude];
    case "down":
      return [0, magnitude];
    case "left":
      return [-magnitude, 0];
    case "right":
      return [magnitude, 0];
  }
}
