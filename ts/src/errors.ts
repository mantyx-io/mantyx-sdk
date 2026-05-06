/**
 * Error types raised by the MANTYX SDK.
 */

export class MantyxError extends Error {
  readonly code: string;
  readonly status: number | undefined;
  readonly hint: string | undefined;

  constructor(
    message: string,
    opts: { code?: string; status?: number; hint?: string } = {},
  ) {
    super(message);
    this.name = "MantyxError";
    this.code = opts.code ?? "mantyx_error";
    this.status = opts.status;
    this.hint = opts.hint;
  }
}

export class MantyxNetworkError extends MantyxError {
  constructor(message: string, opts: { cause?: unknown } = {}) {
    super(message, { code: "network" });
    this.name = "MantyxNetworkError";
    if (opts.cause !== undefined) {
      (this as Error & { cause?: unknown }).cause = opts.cause;
    }
  }
}

export class MantyxAuthError extends MantyxError {
  constructor(message = "Invalid or missing API key") {
    super(message, { code: "unauthorized", status: 401 });
    this.name = "MantyxAuthError";
  }
}

export class MantyxToolError extends MantyxError {
  readonly toolName: string;

  constructor(toolName: string, message: string) {
    super(`Local tool ${JSON.stringify(toolName)} failed: ${message}`, {
      code: "local_tool_failed",
    });
    this.name = "MantyxToolError";
    this.toolName = toolName;
  }
}

export class MantyxRunError extends MantyxError {
  readonly runId: string;
  readonly subtype: string;

  constructor(runId: string, subtype: string, message: string) {
    super(message, { code: subtype });
    this.name = "MantyxRunError";
    this.runId = runId;
    this.subtype = subtype;
  }
}

/**
 * Thrown by {@link parseRunOutput} when the run's terminal text was supposed
 * to be a JSON document (because `outputSchema` was set on the spec) but
 * either failed to JSON.parse or failed the user-supplied validator.
 *
 * The original `text` is preserved on the `text` field so callers can log
 * the raw model output for debugging.
 */
export class MantyxParseError extends MantyxError {
  readonly text: string;

  constructor(message: string, text: string, opts: { cause?: unknown } = {}) {
    super(message, { code: "output_parse_failed" });
    this.name = "MantyxParseError";
    this.text = text;
    if (opts.cause !== undefined) {
      (this as Error & { cause?: unknown }).cause = opts.cause;
    }
  }
}
