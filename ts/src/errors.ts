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

/**
 * Optional triage attributes the runner attaches to terminal `error`
 * events. Mirrors the wire fields described in
 * `docs/agent-runs-protocol.md` §7 ("error event payload fields") so SDK
 * callers can render structured UI status notes ("model truncated — JSON
 * likely incomplete") and drive retry policy without re-parsing the
 * human-readable `message`.
 */
export interface MantyxRunErrorInit {
  /**
   * Canonical category of failure. One of `"rate_limit"`, `"overloaded"`,
   * `"server"`, `"context_window"`, `"truncation"`, `"invalid_request"`,
   * `"auth"`, `"timeout"`, `"local_timeout"`, `"upstream_deadline"`,
   * `"unknown"`. New categories may land additively — callers should
   * default-branch to `"unknown"` for unrecognized values.
   */
  errorClass?: string;
  /**
   * Canonical lowercase stop reason normalized across providers
   * (`"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). When
   * present, mirrors the value carried on the last `assistant_message`
   * event preceding the failure.
   */
  finishReason?: string | null;
  /**
   * **Best-effort raw bytes** the model emitted before the failure. For
   * `outputSchema` runs this is likely **incomplete JSON** that will
   * fail `JSON.parse` — treat it as diagnostic data, never as a
   * schema-conformant reply.
   */
  partialText?: string;
  /**
   * Coarse retry hint inherited from the pipeline's error classifier.
   * Informational; the SDK still owns the actual retry decision.
   */
  retryable?: boolean;
}

export class MantyxRunError extends MantyxError {
  readonly runId: string;
  readonly subtype: string;
  /** See {@link MantyxRunErrorInit.errorClass}. */
  readonly errorClass: string | undefined;
  /** See {@link MantyxRunErrorInit.finishReason}. */
  readonly finishReason: string | null | undefined;
  /** See {@link MantyxRunErrorInit.partialText}. */
  readonly partialText: string | undefined;
  /** See {@link MantyxRunErrorInit.retryable}. */
  readonly retryable: boolean | undefined;

  constructor(
    runId: string,
    subtype: string,
    message: string,
    init: MantyxRunErrorInit = {},
  ) {
    super(message, { code: subtype });
    this.name = "MantyxRunError";
    this.runId = runId;
    this.subtype = subtype;
    this.errorClass = init.errorClass;
    this.finishReason = init.finishReason;
    this.partialText = init.partialText;
    this.retryable = init.retryable;
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
