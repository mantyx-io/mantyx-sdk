/**
 * MANTYX SDK client: HTTP plumbing, model catalog, run + session drivers.
 */
import {
  MantyxAuthError,
  MantyxError,
  MantyxNetworkError,
  MantyxParseError,
  MantyxRunError,
  MantyxScopeError,
  MantyxToolError,
} from "./errors.js";
import type { MantyxRunErrorInit } from "./errors.js";
import { callA2A, callMcpTool, closeMcpRefs, resolveLocalRefs } from "./local-resolver.js";
import type { TokenSource } from "./oauth.js";
import { readSseStream } from "./sse.js";
import type {
  LocalA2ATool,
  LocalMcpServer,
  LocalTool,
  ReasoningLevel,
  ToolRef,
} from "./tools.js";
import { isLocalA2ATool, isLocalMcpServer, isLocalTool, prefixedMcpToolName } from "./tools.js";
import { toToolParametersWire } from "./zod-to-json-schema.js";

export const DEFAULT_BASE_URL = "https://app.mantyx.io";

export interface MantyxClientOptions {
  /**
   * Workspace API key (token prefix `mantyx_`) **or** a MANTYX OAuth 2.0
   * access token (token prefix `mantyx_at_`). The server resolves either
   * kind by token-prefix, so the SDK uses a single credential code path.
   *
   * Prefer the {@link accessToken} alias when wiring up an OAuth-based
   * application — the two options are semantically identical (the value
   * is forwarded as `Authorization: Bearer <credential>`), but
   * `accessToken` makes the intent obvious at the call site.
   *
   * Exactly one of `apiKey` / `accessToken` must be set. Passing both —
   * even to the same value — throws `MantyxError` at construction time.
   *
   * See `docs/agent-runs-protocol.md` §2 for the full credential table
   * (including which prefix means what, scope semantics, and the
   * `insufficient_scope` 403 SDKs surface via
   * {@link MantyxScopeError}).
   */
  apiKey?: string;
  /**
   * MANTYX OAuth 2.0 access token (token prefix `mantyx_at_…`). Exactly
   * one of {@link apiKey} / `accessToken` / {@link tokenSource} must be
   * set; passing more than one throws `MantyxError` at construction
   * time.
   *
   * Functionally identical to {@link apiKey} — the SDK ships either
   * value verbatim on `Authorization: Bearer <credential>` — but using
   * the OAuth-specific name makes scope-driven applications easier to
   * read.
   *
   * OAuth tokens additionally enforce per-route **scopes**
   * (`runs:read`, `runs:write`, `sessions:read`, `sessions:write`,
   * `models:read`, `mantyx.identity:read`); see
   * `docs/agent-runs-protocol.md` §2.2 for the table. Missing scopes
   * land as {@link MantyxScopeError} so callers can route the user
   * back to a re-consent flow.
   *
   * Static `accessToken` values are 1-hour-lived per `docs/oauth.md`
   * §"Token lifetimes & lifecycle" — for long-running processes
   * prefer {@link tokenSource} so the SDK can refresh transparently.
   */
  accessToken?: string;
  /**
   * Dynamic credential provider. The SDK calls it before every request
   * to obtain the current access token, and again with
   * `reason: "unauthorized"` after a 401 so it can refresh and retry
   * the request exactly once.
   *
   * Build one via `oauthClient.refreshTokenSource({ refreshToken })`
   * or `oauthClient.clientCredentialsTokenSource()` — see
   * [`./oauth.ts`](./oauth.ts) for the helpers, or pass any function
   * matching the {@link TokenSource} signature for full custom
   * control (e.g. tokens minted by an upstream auth proxy).
   *
   * Exactly one of {@link apiKey} / {@link accessToken} / `tokenSource`
   * must be set.
   */
  tokenSource?: TokenSource;
  workspaceSlug: string;
  /** Defaults to `https://app.mantyx.io`. Override for self-hosted instances. */
  baseUrl?: string;
  /** Optional `fetch` override (e.g. node-fetch wrapper, or a custom HTTP client). */
  fetch?: typeof fetch;
  /** Default per-request timeout in milliseconds. Default: 60s. */
  timeoutMs?: number;
}

export interface ModelInfo {
  id: string;
  label: string;
  provider: string;
  vendorModelId: string;
  source: "workspace_provider" | "platform_offering";
  contextWindowTokens: number | null;
  pricing: {
    inputPer1MUsd: number | null;
    outputPer1MUsd: number | null;
    cacheReadPer1MUsd: number | null;
  } | null;
}

export interface ModelCatalog {
  models: ModelInfo[];
  defaultModelId: string | null;
}

export interface AgentSpecBase {
  name?: string;
  /**
   * Reference to a persisted MANTYX agent in this workspace. When set, the
   * server hydrates `systemPrompt`, `modelId`, and the agent's own tools
   * (memory, skills, plugin tools, …) from the Agent row at run time, and any
   * `tools` you supply here are merged on top — typically `local` tools the
   * SDK wants the agent to be able to call back into.
   *
   * Either `agentId` or `systemPrompt` must be set.
   */
  agentId?: string;
  /** Required unless `agentId` is set. */
  systemPrompt?: string;
  modelId?: string;
  tools?: ToolRef[];
  /**
   * Provider thinking strength: a string anchor (`"off" | "low" | "medium" |
   * "high"`) or an integer in `0..100` (where `0` explicitly disables provider
   * thinking on reasoning models). The server maps this onto each LLM's
   * native dial — see `docs/agent-runs-protocol.md` §4.4.
   *
   * For session-scoped runs the session value sets the default; per-message
   * overrides on `session.send` apply to that single run.
   */
  reasoningLevel?: ReasoningLevel;
  budgets?: { maxToolTurns?: number };
  /**
   * Constrains the model's **final assistant text** to a JSON document
   * matching a JSON Schema. The terminal `result` event still carries the
   * reply as `text: string`, but that string is guaranteed-parseable JSON.
   *
   * `name` (optional) is a stable identifier the server forwards to the
   * provider (OpenAI `text.format.name`, Anthropic synthetic-tool name).
   * Defaults to `"output"`. Must match `/^[a-zA-Z0-9_-]{1,64}$/`.
   *
   * `schema` is a JSON Schema describing the final assistant text. Its
   * root must be a JSON **object** — most providers reject array / scalar
   * roots in structured-output mode. The schema is shipped verbatim;
   * MANTYX does not validate its contents (the provider does).
   *
   * Use {@link parseRunOutput} on the resulting `RunResult` to JSON.parse
   * the reply (and optionally re-validate against your own zod / typebox /
   * ajv schema). See `docs/wire-protocol.md` §7.
   */
  outputSchema?: OutputSchema;
  /**
   * Loop-detection guard. Tracks an order-invariant `(toolName, args)`
   * signature for every assistant turn that emits one or more tool calls;
   * when the same signature repeats consecutively the pipeline first injects
   * a steering nudge ("either deliver a final answer or change strategy")
   * and eventually forces a tools-disabled finalise turn.
   *
   * Pass an object to override the default thresholds, or `false` to
   * explicitly disable the guard for this run / session. When omitted, the
   * MANTYX runtime defaults apply (`{ consecutiveThreshold: 3,
   * hardCutoffThreshold: 6 }`). See `docs/agent-runs-protocol.md` §4.6.
   *
   * Each intervention emits an observability-only `loop_detected` SSE event
   * the SDK surfaces on the run-event stream (`tools` lists the looping
   * batch; `hardCutoff: false` is the soft nudge round, `true` is the
   * forced finalise). The synthetic skip + nudge are emitted on the normal
   * `tool_result` / `assistant_delta` channels — the SDK does not need to
   * act on the event itself.
   */
  loopDetection?: LoopDetection | false;
  /**
   * Per-tool call caps enforced over the **lifetime of the run** (across
   * every LLM turn). Calls under the cap run normally; calls past the cap
   * are intercepted before execution and returned to the model as a
   * synthetic "budget exceeded — pivot or finalize" tool result.
   *
   * Keys are the model-facing tool names (the same string on
   * `local_tool_call.name`); values are `{ maxCalls: number }`. `maxCalls:
   * 0` disables the tool entirely (the first attempt returns the synthetic
   * body). Budgets are **per-tool, not pooled**.
   *
   * Pass `{}` to start from a clean slate (no defaults applied on top —
   * useful for runs that intentionally want unbounded research). Omit
   * entirely to keep the runtime defaults. Each interception emits an
   * observability-only `tool_budget_exceeded` SSE event. See
   * `docs/agent-runs-protocol.md` §4.7.
   */
  toolBudgets?: ToolBudgets;
  /**
   * Flat string→string KV carried alongside the run / session for
   * observability. Use it to tag runs with your own application identifiers
   * (customer id, environment, workflow name, …) — the values are visible in
   * the MANTYX dashboard and can be filtered there.
   *
   * Limits enforced server-side: max 16 entries; keys match
   * `[A-Za-z0-9._-]{1,64}`; values are strings ≤ 256 chars; serialized JSON
   * ≤ 4 KB. For session-scoped runs, the session's metadata is inherited and
   * any per-message override is merged on top.
   */
  metadata?: Record<string, string>;
}

export interface RunSpec extends AgentSpecBase {
  prompt?: string;
  messages?: Array<{ role: "user" | "assistant" | "system"; content: string }>;
  /** Receives streaming assistant text deltas. */
  onAssistantDelta?: (delta: string) => void;
  /** Receives raw events (assistant_message, local_tool_call, tool_result, ...) for advanced consumers. */
  onEvent?: (event: RunEvent) => void;
  /** Aborts the run on the client and best-effort cancels server-side. */
  signal?: AbortSignal;
}

export type SessionSpec = AgentSpecBase;

/**
 * Constrains the final assistant text to a JSON document matching a
 * JSON Schema. See {@link AgentSpecBase.outputSchema} for the full
 * semantics.
 */
export interface OutputSchema {
  /** Optional. Defaults to `"output"`. Must match `/^[a-zA-Z0-9_-]{1,64}$/`. */
  name?: string;
  /** Required. JSON Schema describing the final assistant text. Root must be a JSON object. */
  schema: Record<string, unknown>;
}

/**
 * Loop-detection thresholds. See {@link AgentSpecBase.loopDetection} for the
 * full semantics. Pass `false` (instead of an object) to disable the guard.
 *
 * Both fields are optional; omitted ones inherit the MANTYX runtime
 * defaults (`consecutiveThreshold: 3`, `hardCutoffThreshold: 6`).
 */
export interface LoopDetection {
  /**
   * Number of identical consecutive tool-call batches that triggers the
   * **soft nudge** — the pipeline injects a steering message ("either
   * deliver a final answer or change strategy"). Default `3`. Must be
   * `>= 2` (one identical batch is just a single tool call, not a loop).
   * Server-side upper bound: `100`.
   */
  consecutiveThreshold?: number;
  /**
   * Number of identical consecutive tool-call batches that triggers the
   * **hard cutoff** — the pipeline forces a tools-disabled finalise turn.
   * Default `6`. Must be strictly greater than `consecutiveThreshold` (so
   * the soft nudge has a chance to land). Server-side upper bound: `100`.
   */
  hardCutoffThreshold?: number;
}

/**
 * Per-tool call cap. See {@link AgentSpecBase.toolBudgets} for the full
 * semantics.
 */
export interface ToolBudget {
  /**
   * Hard cap on executed calls per run. `0` disables the tool entirely
   * (every attempt returns the synthetic "budget exceeded" body on the
   * first try). Server-side upper bound: `1000` (functionally unlimited;
   * the in-runtime `maxToolTurns: 100` fires first).
   */
  maxCalls: number;
}

/**
 * Map of model-facing tool name → cap. See
 * {@link AgentSpecBase.toolBudgets}. Pass an empty object (`{}`) to start
 * from a clean slate (no runtime defaults applied on top); omit the field
 * entirely to keep the defaults.
 */
export type ToolBudgets = Record<string, ToolBudget>;

/**
 * Per-run token totals attached to terminal `result` / `error` events
 * (and to the `GET /agent-runs/:runId` snapshot) by MANTYX ≥ 2026-09.
 *
 * Aggregated across every model invocation for the run. See
 * `docs/agent-runs-protocol.md` §7.1 for the per-provider mapping and
 * the relationship between buckets (`inputTokens` / `outputTokens` are
 * the billable totals; `cachedTokens` and `reasoningTokens` are
 * diagnostic breakdowns _inside_ those two totals, not separate
 * additive buckets).
 *
 * Older servers omit the cost-attribution triple entirely; SDK callers
 * detect "no usage data" by checking `result.model?.provider` is empty
 * / undefined.
 */
export interface RunTokenUsage {
  /**
   * Total billable input tokens — fresh prompt tokens plus the
   * cached-read slice the provider still bills (at a discount) plus
   * any cache-creation tokens plus tool-prompt tokens. Equal to the
   * sum of every provider-reported input bucket for the run.
   */
  inputTokens: number;
  /**
   * The discounted slice of `inputTokens` that came from a prompt
   * cache hit (Anthropic prompt caching, OpenAI cached prompt, Gemini
   * implicit cache). `0` when the provider doesn't report cache reads
   * or the run didn't hit cache.
   */
  cachedTokens: number;
  /**
   * Non-visible thinking tokens. **Already counted inside
   * `outputTokens`** — surfaced separately so dashboards can break out
   * "thinking cost" vs visible output. `0` when the model didn't
   * reason or didn't report it.
   */
  reasoningTokens: number;
  /**
   * All tokens the model emitted for this run, visible + reasoning.
   * Matches the provider's "completion tokens" / "output tokens"
   * billing line.
   */
  outputTokens: number;
}

/**
 * The resolved model the platform stamped onto the run, surfaced on
 * terminal `result` / `error` events (and `GET /agent-runs/:runId`)
 * by MANTYX ≥ 2026-09. See `docs/agent-runs-protocol.md` §7.1.
 */
export interface RunModelInfo {
  /**
   * Catalog id — the same string a caller would pass back as
   * `modelId` to re-select this exact entry (e.g. `"platform:demo"`,
   * `"provider:cmf…"`). Empty string against legacy fallbacks that
   * didn't synthesise a catalog id.
   */
  id: string;
  /**
   * Lowercase provider id: `"openai"`, `"anthropic"`, `"google"`,
   * `"azure-openai"`. Empty string against legacy runners that don't
   * report usage data — SDK callers use that as the "no usage data"
   * signal.
   */
  provider: string;
  /**
   * The model id the platform actually sent to the provider (e.g.
   * `"gpt-5.4-mini"`, `"claude-opus-4-7"`, `"gemini-2.5-pro"`).
   */
  vendorModelId: string;
  /**
   * `"off" | "low" | "medium" | "high"`. Omitted when the provider
   * doesn't expose a reasoning-level knob or the run didn't request
   * one.
   */
  reasoningEffort?: string;
}

export interface RunResult {
  runId: string;
  text: string;
  events: RunEvent[];
  /**
   * Per-run token totals from the terminal event. Undefined against
   * MANTYX servers older than 2026-09 (the "no usage data" signal is
   * `result.model?.provider` being empty / undefined). See
   * {@link RunTokenUsage} and `docs/agent-runs-protocol.md` §7.1.
   */
  tokens?: RunTokenUsage;
  /**
   * Total `engine.completeTurn(...)` invocations for the run,
   * including the failing call when a run errored mid-loop. A
   * single-shot run reports `1`; a tool loop is `>= 2`. Undefined
   * against legacy MANTYX servers.
   */
  turns?: number;
  /** Resolved model that executed the run. See {@link RunModelInfo}. */
  model?: RunModelInfo;
}

export interface RunEventBase {
  seq: number;
  type: string;
}

export interface AssistantDeltaEvent extends RunEventBase {
  type: "assistant_delta";
  text: string;
}

export interface ThinkingDeltaEvent extends RunEventBase {
  type: "thinking_delta";
  text: string;
}

export interface AssistantMessageEvent extends RunEventBase {
  type: "assistant_message";
  /**
   * Full assistant text for this turn (concatenation of every preceding
   * `assistant_delta` for the turn, plus any non-streaming snapshot the
   * engine appended at close). May be empty when the turn was tool-only.
   */
  text: string;
  /**
   * 0-based tool-turn index this assistant message closes. Useful for
   * SDK clients pairing the message with the subsequent `tool_result`
   * rows.
   */
  turn?: number;
  /**
   * Canonical lowercase stop reason normalized across providers
   * (`"end_turn"`, `"tool_use"`, `"max_tokens"`, `"refusal"`,
   * `"malformed_function_call"`, …). `null` / omitted when the provider
   * did not report one.
   */
  finishReason?: string | null;
  /**
   * Tool calls the model emitted on this turn. Omitted when the model
   * did not call any tools.
   */
  toolCalls?: Array<{
    id: string;
    name: string;
    input: Record<string, unknown>;
  }>;
}

export interface ServerToolResultEvent extends RunEventBase {
  type: "tool_result";
  name: string;
  args?: Record<string, unknown>;
  ok?: boolean;
  summary?: string;
  phase?: "start" | "end";
}

export interface LocalToolCallEvent extends RunEventBase {
  type: "local_tool_call";
  toolUseId: string;
  /**
   * The model-facing tool name. For `kind: "mcp_local"` events this is the
   * `<server>_<tool>` name the SDK declared on the wire; the SDK looks up
   * the local MCP server via `mcpServer` and forwards `mcpToolName` to
   * `tools/call` rather than parsing the prefix itself.
   */
  name: string;
  args: Record<string, unknown>;
  /**
   * Discriminator for which client-resolved handler should run.
   * - `"local"` (or omitted) — generic local tool
   * - `"a2a_local"` — local Agent2Agent peer
   * - `"mcp_local"` — local MCP server tool
   */
  kind?: "local" | "a2a_local" | "mcp_local";
  /**
   * Present on `kind: "a2a_local"` — the full A2A Agent Card the SDK shipped
   * with the spec, echoed back unchanged. Surfaced for advanced consumers
   * (`onEvent` / `streamAgent` callers); the built-in dispatcher ignores it
   * because it already has the cached card from the original
   * `defineLocalA2A` resolution.
   */
  agentCard?: { name: string; url?: string; [k: string]: unknown };
  /** Present on `kind: "mcp_local"` — server label declared via `defineLocalMcp`. */
  mcpServer?: string;
  /**
   * Present on `kind: "mcp_local"` — the model-facing tool name as declared on
   * the wire. Always equals `name`; surfaced as a separate field for the SDK's
   * convenience when dispatching into a local MCP client.
   */
  mcpToolName?: string;
  /**
   * Present on `kind: "mcp_local"` — the verbatim `Implementation` block from
   * MCP `Initialize`, echoed back for observability.
   */
  mcpServerInfo?: { name: string; version?: string; [k: string]: unknown };
}

export interface LocalToolResultInEvent extends RunEventBase {
  type: "local_tool_result_in";
  toolUseId: string;
  result?: string;
  error?: string;
}

/**
 * Observability event fired when the loop-detection guard intervenes.
 * The synthetic skip + steering nudge are emitted on the normal
 * `tool_result` / `assistant_delta` channels; this event lets the SDK
 * render a status note (`looping — nudged` / `looping — gave up`).
 *
 * `hardCutoff: false` is the soft nudge round; `true` is the forced
 * finalise. The same run may emit one of each.
 */
export interface LoopDetectedEvent extends RunEventBase {
  type: "loop_detected";
  /** Length of the identical-batch streak that just tripped the threshold. */
  consecutiveCount: number;
  /** `false` for the soft nudge round; `true` once the pipeline forces finalisation. */
  hardCutoff: boolean;
  /** Names of the tool calls in the looping batch (no args). */
  tools: string[];
}

/**
 * Observability event fired when a tool-budget interception happens. The
 * synthetic "budget exceeded — pivot or finalize" tool result lands on the
 * normal `tool_result` channel before this event fires; the SDK uses this
 * event to render UI banners (`memory budget exhausted` etc.) without
 * re-parsing tool-result bodies.
 */
export interface ToolBudgetExceededEvent extends RunEventBase {
  type: "tool_budget_exceeded";
  /** Logical tool name (matches the key in `spec.toolBudgets`). */
  tool: string;
  /** Configured cap. */
  maxCalls: number;
  /**
   * 1-based count of attempts to call this tool over the run lifetime.
   * Always strictly greater than `maxCalls`.
   */
  callIndex: number;
}

export interface ResultEvent extends RunEventBase {
  type: "result";
  subtype: string;
  text?: string;
  error?: string;
  /**
   * Per-run token totals. Present against MANTYX ≥ 2026-09 — see
   * {@link RunTokenUsage} and `docs/agent-runs-protocol.md` §7.1.
   */
  tokens?: RunTokenUsage;
  /** Total model invocations for the run. See {@link RunResult.turns}. */
  turns?: number;
  /** Resolved model that executed the run. See {@link RunModelInfo}. */
  model?: RunModelInfo;
}

export interface ErrorEvent extends RunEventBase {
  type: "error";
  /** Human-readable failure message. */
  error: string;
  /**
   * Legacy alias for {@link errorClass}. Equals `errorClass` when present;
   * otherwise a small lowercase token (`"error"`, `"invalid_spec"`,
   * `"worker_error"`, …).
   */
  code?: string;
  /**
   * Canonical failure category. One of `"rate_limit"`, `"overloaded"`,
   * `"server"`, `"context_window"`, `"truncation"`, `"invalid_request"`,
   * `"auth"`, `"timeout"`, `"local_timeout"`, `"upstream_deadline"`,
   * `"unknown"`. New categories may land additively. See
   * `docs/agent-runs-protocol.md` §7 for the full list.
   */
  errorClass?: string;
  /**
   * Canonical lowercase stop reason normalized across providers
   * (`"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). When
   * present, mirrors the value on the last `assistant_message` event.
   */
  finishReason?: string | null;
  /**
   * **Best-effort raw bytes** the model emitted before the failure. For
   * `outputSchema` runs this is likely **incomplete JSON** that will
   * fail `JSON.parse` — see the wire-protocol truncation contract. Also
   * persisted on `EphemeralAgentRun.finalText` so SDKs can recover it
   * via `GET /agent-runs/:runId` after the SSE stream closes.
   */
  partialText?: string;
  /**
   * Coarse retry hint inherited from the pipeline's error classifier.
   * Informational; the SDK still owns the actual retry decision.
   */
  retryable?: boolean;
  /**
   * Per-run token totals. Present against MANTYX ≥ 2026-09 — see
   * {@link RunTokenUsage} and `docs/agent-runs-protocol.md` §7.1.
   * The pipeline counts the failing model call too, so a run that
   * threw on the first turn reports `turns: 1` with that call's
   * tokens already aggregated.
   */
  tokens?: RunTokenUsage;
  /** Total model invocations for the run, including the failing call. */
  turns?: number;
  /** Resolved model that executed the run. See {@link RunModelInfo}. */
  model?: RunModelInfo;
}

export interface CancelledEvent extends RunEventBase {
  type: "cancelled";
  reason?: string;
}

export type RunEvent =
  | AssistantDeltaEvent
  | ThinkingDeltaEvent
  | AssistantMessageEvent
  | ServerToolResultEvent
  | LocalToolCallEvent
  | LocalToolResultInEvent
  | LoopDetectedEvent
  | ToolBudgetExceededEvent
  | ResultEvent
  | ErrorEvent
  | CancelledEvent
  | (RunEventBase & { type: string; [key: string]: unknown });

export interface SessionInfo {
  id: string;
  name: string;
  status: "active" | "ended";
  createdAt: string;
  lastUsedAt: string;
  endedAt: string | null;
  agentSpec: AgentSpecBase;
  messages: Array<{ role: "user" | "assistant" | "system"; content: string }>;
  /** Metadata that was attached to the session at create time, returned for observability. */
  metadata: Record<string, string>;
}

export class MantyxClient {
  readonly options: Required<Pick<MantyxClientOptions, "workspaceSlug" | "baseUrl">> & {
    /**
     * Single resolved bearer credential — either a workspace API key
     * (token prefix `mantyx_`) or an OAuth access token (`mantyx_at_…`).
     * The SDK does not need to distinguish them on the wire; the value
     * is forwarded verbatim on `Authorization: Bearer …`.
     *
     * Kept as `apiKey` (instead of e.g. `credential`) for backwards
     * compatibility — older releases exposed it under this name.
     *
     * Empty string when a {@link tokenSource} is configured — every
     * request resolves the bearer from the source instead.
     */
    apiKey: string;
    fetch: typeof fetch;
    timeoutMs: number;
    /**
     * Dynamic credential provider when constructed with
     * `tokenSource` — see {@link MantyxClientOptions.tokenSource}.
     * `null` for static `apiKey` / `accessToken` clients.
     */
    tokenSource: TokenSource | null;
  };

  constructor(opts: MantyxClientOptions) {
    const { credential, tokenSource } = resolveCredential(opts);
    if (!opts.workspaceSlug || typeof opts.workspaceSlug !== "string") {
      throw new MantyxError("workspaceSlug is required");
    }
    const f = opts.fetch ?? globalThis.fetch;
    if (typeof f !== "function") {
      throw new MantyxError(
        "Global fetch is not available; pass a custom `fetch` implementation in MantyxClientOptions.",
      );
    }
    this.options = {
      apiKey: credential,
      workspaceSlug: opts.workspaceSlug,
      baseUrl: (opts.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, ""),
      fetch: f,
      timeoutMs: opts.timeoutMs ?? 60_000,
      tokenSource,
    };
  }

  // -------------------------------------------------------------- Models

  async listModels(): Promise<ModelCatalog> {
    return this.request<ModelCatalog>({
      method: "GET",
      path: "/models",
    });
  }

  // ------------------------------------------------------------- One-shot

  async runAgent(spec: RunSpec): Promise<RunResult> {
    const tools = spec.tools ?? [];
    // Resolve every `a2a_local` agent card and open every `mcp_local`
    // transport before submitting; the resolver mutates the refs in place
    // so the subsequent `serializeAgentSpec` reads the resolved data.
    await resolveLocalRefs(tools, { fetch: this.options.fetch });
    const handlers = collectLocalHandlers(tools);
    try {
      const created = await this.request<{ runId: string; streamUrl: string }>({
        method: "POST",
        path: "/agent-runs",
        body: serializeAgentSpec(spec, {
          prompt: spec.prompt,
          messages: spec.messages,
        }),
      });
      return await this.driveRun(created.runId, handlers, {
        ...(spec.onAssistantDelta ? { onAssistantDelta: spec.onAssistantDelta } : {}),
        ...(spec.onEvent ? { onEvent: spec.onEvent } : {}),
        ...(spec.signal ? { signal: spec.signal } : {}),
      });
    } finally {
      // One-shot runs own their MCP transports; close them on exit.
      await closeMcpRefs(tools);
    }
  }

  async *streamAgent(spec: RunSpec): AsyncGenerator<RunEvent, void, void> {
    const tools = spec.tools ?? [];
    await resolveLocalRefs(tools, { fetch: this.options.fetch });
    const handlers = collectLocalHandlers(tools);
    try {
      const created = await this.request<{ runId: string; streamUrl: string }>({
        method: "POST",
        path: "/agent-runs",
        body: serializeAgentSpec(spec, {
          prompt: spec.prompt,
          messages: spec.messages,
        }),
      });
      yield* this.streamRunEvents(created.runId, handlers, spec.signal);
    } finally {
      await closeMcpRefs(tools);
    }
  }

  /**
   * Internal registry of client-resolved tool handlers. Exposed for callers
   * who drive the run loop manually via `driveRun` / `streamRunEvents`.
   */
  collectHandlers(tools: ToolRef[]): LocalHandlers {
    return collectLocalHandlers(tools);
  }

  // ------------------------------------------------------------- Sessions

  async createSession(spec: SessionSpec): Promise<AgentSession> {
    const tools = spec.tools ?? [];
    // Resolve local refs once at session creation; the session keeps the
    // resolved cards / live MCP connections for its lifetime.
    await resolveLocalRefs(tools, { fetch: this.options.fetch });
    const handlers = collectLocalHandlers(tools);
    const created = await this.request<{ sessionId: string; name: string; createdAt: string }>({
      method: "POST",
      path: "/agent-sessions",
      body: serializeAgentSpec(spec),
    });
    return new AgentSession(this, created.sessionId, handlers, tools);
  }

  /**
   * Re-emit a `local_tool_call` event into the right local handler. Useful
   * for tests and for users who consume events via `streamAgent` themselves.
   */
  async dispatchLocalToolFromEvent(
    runId: string,
    ev: LocalToolCallEvent,
    handlers: LocalHandlers,
  ): Promise<void> {
    return this.dispatchLocalTool(runId, ev, handlers);
  }

  async resumeSession(
    sessionId: string,
    opts: { tools?: ToolRef[] } = {},
  ): Promise<AgentSession> {
    // Verify the session exists and is still active. Optionally refresh tool defs.
    await this.getSessionInfo(sessionId);
    const tools = opts.tools ?? [];
    if (tools.length > 0) {
      // Resolve before the first send — mirrors createSession.
      await resolveLocalRefs(tools, { fetch: this.options.fetch });
    }
    const handlers = collectLocalHandlers(tools);
    return new AgentSession(this, sessionId, handlers, tools);
  }

  async endSession(sessionId: string): Promise<void> {
    await this.request<{ ok: boolean }>({
      method: "DELETE",
      path: `/agent-sessions/${encodeURIComponent(sessionId)}`,
    });
  }

  async getSessionInfo(sessionId: string): Promise<SessionInfo> {
    return this.request<SessionInfo>({
      method: "GET",
      path: `/agent-sessions/${encodeURIComponent(sessionId)}`,
    });
  }

  // ----------------------------------------------------------- Internals

  /** Drive an existing run to completion (collect events, dispatch local tools). */
  async driveRun(
    runId: string,
    handlers: LocalHandlers,
    opts: {
      onAssistantDelta?: (delta: string) => void;
      onEvent?: (event: RunEvent) => void;
      signal?: AbortSignal;
    } = {},
  ): Promise<RunResult> {
    const collected: RunEvent[] = [];
    let finalText = "";
    // Cost-attribution triple, populated from the terminal event when
    // MANTYX ≥ 2026-09 surfaces it. Older runners omit the fields and
    // we leave the result's `tokens` / `turns` / `model` undefined —
    // callers detect "no usage data" via `result.model?.provider`.
    let tokens: RunTokenUsage | undefined;
    let turns: number | undefined;
    let modelInfo: RunModelInfo | undefined;
    for await (const ev of this.streamRunEvents(runId, handlers, opts.signal)) {
      collected.push(ev);
      if (opts.onEvent) opts.onEvent(ev);
      if (ev.type === "assistant_delta" && opts.onAssistantDelta) {
        opts.onAssistantDelta((ev as AssistantDeltaEvent).text);
      }
      if (ev.type === "result") {
        const r = ev as ResultEvent;
        tokens = parseRunTokens(r.tokens) ?? tokens;
        turns = parseRunTurns(r.turns) ?? turns;
        modelInfo = parseRunModel(r.model) ?? modelInfo;
        if (r.subtype === "success") {
          finalText = typeof r.text === "string" ? r.text : "";
        } else {
          const errInit: MantyxRunErrorInit = {};
          if (tokens !== undefined) errInit.tokens = tokens;
          if (turns !== undefined) errInit.turns = turns;
          if (modelInfo !== undefined) errInit.model = modelInfo;
          throw new MantyxRunError(runId, r.subtype, r.error ?? r.subtype, errInit);
        }
      } else if (ev.type === "error") {
        const e = ev as ErrorEvent;
        // The wire reports both a coarse `code` (legacy alias) and a
        // canonical `errorClass` triage category; prefer `errorClass`
        // when present so the SDK exposes a stable taxonomy. See
        // `docs/agent-runs-protocol.md` §7.
        const subtype = e.errorClass ?? e.code ?? "error";
        const errInit: MantyxRunErrorInit = {};
        if (e.errorClass !== undefined) errInit.errorClass = e.errorClass;
        if (e.finishReason !== undefined) errInit.finishReason = e.finishReason;
        if (typeof e.partialText === "string") errInit.partialText = e.partialText;
        if (typeof e.retryable === "boolean") errInit.retryable = e.retryable;
        const errTokens = parseRunTokens(e.tokens);
        if (errTokens !== undefined) errInit.tokens = errTokens;
        const errTurns = parseRunTurns(e.turns);
        if (errTurns !== undefined) errInit.turns = errTurns;
        const errModel = parseRunModel(e.model);
        if (errModel !== undefined) errInit.model = errModel;
        throw new MantyxRunError(runId, subtype, e.error, errInit);
      } else if (ev.type === "cancelled") {
        throw new MantyxRunError(runId, "cancelled", "Run was cancelled");
      }
    }
    const result: RunResult = { runId, text: finalText, events: collected };
    if (tokens !== undefined) result.tokens = tokens;
    if (turns !== undefined) result.turns = turns;
    if (modelInfo !== undefined) result.model = modelInfo;
    return result;
  }

  async *streamRunEvents(
    runId: string,
    handlers: LocalHandlers,
    signal?: AbortSignal,
  ): AsyncGenerator<RunEvent, void, void> {
    const url = this.absoluteUrl(`/agent-runs/${encodeURIComponent(runId)}/stream`);
    let lastSeq = 0;
    while (true) {
      const reqUrl = lastSeq > 0 ? `${url}?lastSeq=${lastSeq}` : url;
      const res = await this.openSseStream(reqUrl, lastSeq, signal);
      if (!res.ok) {
        throw await this.errorFromResponse(res);
      }
      let terminal = false;
      try {
        for await (const sseEvent of readSseStream(res.body, { ...(signal ? { signal } : {}) })) {
          let data: Record<string, unknown> = {};
          try {
            data = JSON.parse(sseEvent.data || "{}") as Record<string, unknown>;
          } catch {
            data = {};
          }
          const evType = sseEvent.event ?? (data.type as string | undefined) ?? "message";
          const seq = typeof data.seq === "number" ? data.seq : lastSeq;
          if (typeof seq === "number" && seq > lastSeq) lastSeq = seq;
          const ev = { seq, type: evType, ...data } as RunEvent;
          yield ev;
          if (evType === "local_tool_call") {
            const localEv = ev as LocalToolCallEvent;
            void this.dispatchLocalTool(runId, localEv, handlers).catch((err) => {
              // best-effort logging; the run will surface a `result/error` if the
              // server eventually times out.
              console.error("[mantyx-sdk] local tool dispatch failed:", err);
            });
          }
          if (evType === "result" || evType === "error" || evType === "cancelled") {
            terminal = true;
            return;
          }
        }
      } catch (err) {
        if (signal?.aborted) {
          throw new MantyxRunError(runId, "cancelled", "Run was cancelled by the client");
        }
        // Network blip — retry after a tiny backoff with `?lastSeq=`.
        await sleep(500);
        continue;
      }
      if (terminal) return;
      // Stream closed without a terminal event (server restart, etc.) — reconnect.
    }
  }

  async dispatchLocalTool(
    runId: string,
    ev: LocalToolCallEvent,
    handlers: LocalHandlers,
  ): Promise<void> {
    const kind = ev.kind ?? "local";
    try {
      let out: string;
      if (kind === "a2a_local") {
        const tool = handlers.a2aTools.get(ev.name);
        if (!tool) {
          await this.postToolResult(runId, ev.toolUseId, {
            error: `No local A2A handler registered for tool ${JSON.stringify(ev.name)}`,
          });
          return;
        }
        const message = typeof ev.args?.message === "string" ? (ev.args.message as string) : "";
        out = await callA2A(tool, { message }, { fetch: this.options.fetch });
      } else if (kind === "mcp_local") {
        const serverName = ev.mcpServer ?? "";
        const mcpToolName = ev.mcpToolName ?? "";
        const server = handlers.mcpServers.get(serverName);
        if (!server) {
          await this.postToolResult(runId, ev.toolUseId, {
            error: `No local MCP server registered as ${JSON.stringify(serverName)}`,
          });
          return;
        }
        // The wire-prefixed tool name (`<server>_<tool>`) is what the model
        // sees; the upstream MCP server uses the bare name. Strip the prefix
        // before forwarding to `tools/call`.
        const upstreamName = mcpToolName.startsWith(`${serverName}_`)
          ? mcpToolName.slice(serverName.length + 1)
          : mcpToolName;
        out = await callMcpTool(server, upstreamName, ev.args ?? {});
      } else {
        const handler = handlers.localTools.get(ev.name);
        if (!handler) {
          await this.postToolResult(runId, ev.toolUseId, {
            error: `No local handler registered for tool ${JSON.stringify(ev.name)}`,
          });
          return;
        }
        const args = handler.parameters
          ? (handler.parameters.parse?.(ev.args) as Record<string, unknown>) ?? ev.args
          : ev.args;
        const result = await handler.execute(args);
        out = typeof result === "string" ? result : JSON.stringify(result);
      }
      await this.postToolResult(runId, ev.toolUseId, { result: out });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const handlerName = describeHandlerName(ev);
      await this.postToolResult(runId, ev.toolUseId, {
        error: new MantyxToolError(handlerName, message).message,
      });
    }
  }

  async postToolResult(
    runId: string,
    toolUseId: string,
    payload: { result?: string; error?: string },
  ): Promise<void> {
    await this.request<{ ok: boolean }>({
      method: "POST",
      path: `/agent-runs/${encodeURIComponent(runId)}/tool-results`,
      body: { toolUseId, ...payload },
    });
  }

  async cancelRun(runId: string): Promise<void> {
    await this.request<{ ok: boolean }>({
      method: "POST",
      path: `/agent-runs/${encodeURIComponent(runId)}/cancel`,
    });
  }

  // -------------------------------------------------------------- HTTP

  private absoluteUrl(path: string): string {
    return `${this.options.baseUrl}/api/v1/workspaces/${encodeURIComponent(this.options.workspaceSlug)}${path}`;
  }

  /**
   * Resolve the bearer credential to send on the next request. With a
   * static `apiKey` / `accessToken` this is a synchronous reach into
   * `options.apiKey`; with a {@link TokenSource} it delegates so the
   * source can refresh expired access tokens before we hit the wire.
   *
   * The `reason` is forwarded to the source verbatim. Pass
   * `"unauthorized"` immediately after a 401 so the source forces a
   * refresh rather than handing back its (now-invalid) cached value.
   */
  private async resolveBearer(reason: "initial" | "unauthorized" = "initial"): Promise<string> {
    if (this.options.tokenSource) return this.options.tokenSource(reason);
    return this.options.apiKey;
  }

  /**
   * Open an SSE stream against `reqUrl` with at-most-one refresh +
   * retry on 401. The caller is responsible for the subsequent
   * `readSseStream` loop; this helper only handles the initial GET.
   * Mid-stream 401s propagate as `MantyxNetworkError` from the read
   * loop and trigger a reconnect via the outer `while` in
   * {@link streamRunEvents}.
   */
  private async openSseStream(
    reqUrl: string,
    lastSeq: number,
    signal: AbortSignal | undefined,
  ): Promise<Response> {
    const openOnce = async (reason: "initial" | "unauthorized"): Promise<Response> => {
      const auth = await this.authHeaders(reason);
      return this.options.fetch(reqUrl, {
        method: "GET",
        headers: {
          ...auth,
          Accept: "text/event-stream",
          ...(lastSeq > 0 ? { "Last-Event-ID": String(lastSeq) } : {}),
        },
        ...(signal ? { signal } : {}),
      }).catch((err: unknown) => {
        throw new MantyxNetworkError(`Failed to open SSE stream: ${(err as Error).message}`, {
          cause: err,
        });
      });
    };
    const res = await openOnce("initial");
    if (res.status === 401 && this.options.tokenSource !== null) {
      try {
        await res.text();
      } catch {
        // ignore
      }
      return openOnce("unauthorized");
    }
    return res;
  }

  private async authHeaders(
    reason: "initial" | "unauthorized" = "initial",
  ): Promise<Record<string, string>> {
    const bearer = await this.resolveBearer(reason);
    return { Authorization: `Bearer ${bearer}` };
  }

  async request<T>(args: {
    method: string;
    path: string;
    body?: unknown;
    timeoutMs?: number;
  }): Promise<T> {
    return this.requestWithRetry<T>(args, "initial");
  }

  private async requestWithRetry<T>(
    args: { method: string; path: string; body?: unknown; timeoutMs?: number },
    reason: "initial" | "unauthorized",
  ): Promise<T> {
    const url = this.absoluteUrl(args.path);
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), args.timeoutMs ?? this.options.timeoutMs);
    try {
      const auth = await this.authHeaders(reason);
      const res = await this.options.fetch(url, {
        method: args.method,
        headers: {
          ...auth,
          ...(args.body !== undefined ? { "Content-Type": "application/json" } : {}),
          Accept: "application/json",
        },
        ...(args.body !== undefined ? { body: JSON.stringify(args.body) } : {}),
        signal: ctrl.signal,
      }).catch((err: unknown) => {
        if (ctrl.signal.aborted) {
          throw new MantyxNetworkError(`Request timed out after ${args.timeoutMs ?? this.options.timeoutMs}ms`);
        }
        throw new MantyxNetworkError(`Network error: ${(err as Error).message}`, { cause: err });
      });
      if (!res.ok) {
        // 401 with a configured TokenSource: refresh the access token
        // and retry the original request exactly once. Static-credential
        // clients (no source) fall straight through to `MantyxAuthError`.
        if (
          res.status === 401 &&
          this.options.tokenSource !== null &&
          reason === "initial"
        ) {
          // Drain the body so the socket can be reused.
          try {
            await res.text();
          } catch {
            // ignore
          }
          clearTimeout(t);
          return this.requestWithRetry<T>(args, "unauthorized");
        }
        throw await this.errorFromResponse(res);
      }
      const text = await res.text();
      if (!text) return undefined as unknown as T;
      try {
        return JSON.parse(text) as T;
      } catch (err) {
        throw new MantyxError(`Failed to parse JSON response: ${(err as Error).message}`);
      }
    } finally {
      clearTimeout(t);
    }
  }

  private async errorFromResponse(res: Response): Promise<MantyxError> {
    let body: {
      error?: string;
      code?: string;
      hint?: string;
      required?: string | string[];
    } = {};
    try {
      body = (await res.json()) as typeof body;
    } catch {
      // ignore
    }
    if (res.status === 401) {
      return new MantyxAuthError(body.error ?? "Invalid API key or OAuth access token");
    }
    // `403 insufficient_scope` is the OAuth "missing scope" signal. The
    // server may report `error` or `code` as the discriminator depending
    // on the route; check both. See `docs/agent-runs-protocol.md` §2.3.
    if (res.status === 403 && (body.error === "insufficient_scope" || body.code === "insufficient_scope")) {
      const required = parseRequiredScopes(body.required, res.headers.get("WWW-Authenticate"));
      const msg = required.length > 0
        ? `Missing OAuth scope${required.length > 1 ? "s" : ""}: ${required.join(", ")}`
        : "OAuth access token is missing a required scope";
      return new MantyxScopeError(msg, required);
    }
    return new MantyxError(body.error ?? `HTTP ${res.status}`, {
      code: body.code ?? `http_${res.status}`,
      status: res.status,
      ...(body.hint ? { hint: body.hint } : {}),
    });
  }
}

// ---------------------------------------------------------------- Sessions

export class AgentSession {
  readonly id: string;
  readonly client: MantyxClient;
  private readonly handlers: LocalHandlers;
  private readonly tools: ToolRef[];

  constructor(
    client: MantyxClient,
    id: string,
    handlers: LocalHandlers,
    tools?: ToolRef[],
  ) {
    this.client = client;
    this.id = id;
    this.handlers = handlers;
    this.tools = tools ?? [];
  }

  async send(
    prompt: string,
    opts: {
      onAssistantDelta?: (s: string) => void;
      signal?: AbortSignal;
      /**
       * Per-message metadata override. Server-side this is merged on top of
       * the session's metadata at run-creation time (run-level keys win).
       * Useful for tagging individual turns (e.g. `{ "trace_id": "abc" }`).
       */
      metadata?: Record<string, string>;
      /**
       * Per-message override for `reasoningLevel`. Applies only to this run
       * and does not mutate the session's stored value.
       */
      reasoningLevel?: ReasoningLevel;
      /**
       * Per-message override for `outputSchema`. Applies only to this run
       * and does not mutate the session's stored value.
       */
      outputSchema?: OutputSchema;
      /**
       * Per-message override for `loopDetection`. Applies only to this run
       * and does not mutate the session's stored value. Pass `false` to
       * disable the guard for this single turn.
       */
      loopDetection?: LoopDetection | false;
      /**
       * Per-message override for `toolBudgets`. Applies only to this run
       * and does not mutate the session's stored value.
       */
      toolBudgets?: ToolBudgets;
    } = {},
  ): Promise<RunResult> {
    const created = await this.client.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: `/agent-sessions/${encodeURIComponent(this.id)}/messages`,
      body: this.buildSessionMessageBody(prompt, opts),
    });
    return this.client.driveRun(created.runId, this.handlers, {
      ...(opts.onAssistantDelta ? { onAssistantDelta: opts.onAssistantDelta } : {}),
      ...(opts.signal ? { signal: opts.signal } : {}),
    });
  }

  async *stream(
    prompt: string,
    opts: {
      signal?: AbortSignal;
      metadata?: Record<string, string>;
      reasoningLevel?: ReasoningLevel;
      outputSchema?: OutputSchema;
      loopDetection?: LoopDetection | false;
      toolBudgets?: ToolBudgets;
    } = {},
  ): AsyncGenerator<RunEvent, void, void> {
    const created = await this.client.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: `/agent-sessions/${encodeURIComponent(this.id)}/messages`,
      body: this.buildSessionMessageBody(prompt, opts),
    });
    yield* this.client.streamRunEvents(created.runId, this.handlers, opts.signal);
  }

  private buildSessionMessageBody(
    prompt: string,
    opts: {
      metadata?: Record<string, string>;
      reasoningLevel?: ReasoningLevel;
      outputSchema?: OutputSchema;
      loopDetection?: LoopDetection | false;
      toolBudgets?: ToolBudgets;
    },
  ): Record<string, unknown> {
    const body: Record<string, unknown> = { prompt };
    if (this.tools.length > 0) body.tools = serializeToolRefs(this.tools);
    if (opts.metadata && Object.keys(opts.metadata).length > 0) body.metadata = opts.metadata;
    if (opts.reasoningLevel !== undefined) {
      body.reasoningLevel = normalizeReasoningLevel(opts.reasoningLevel);
    }
    if (opts.outputSchema !== undefined) {
      body.outputSchema = normalizeOutputSchema(opts.outputSchema);
    }
    if (opts.loopDetection !== undefined) {
      body.loopDetection = normalizeLoopDetection(opts.loopDetection);
    }
    if (opts.toolBudgets !== undefined) {
      body.toolBudgets = normalizeToolBudgets(opts.toolBudgets);
    }
    return body;
  }

  async history(): Promise<Array<{ role: "user" | "assistant" | "system"; content: string }>> {
    const info = await this.client.getSessionInfo(this.id);
    return info.messages;
  }

  async info(): Promise<SessionInfo> {
    return this.client.getSessionInfo(this.id);
  }

  async end(): Promise<void> {
    try {
      await this.client.endSession(this.id);
    } finally {
      // Close any MCP transports the session opened.
      await closeMcpRefs(this.tools);
    }
  }
}

// ---------------------------------------------------------------- Helpers

function serializeAgentSpec(
  spec: AgentSpecBase,
  extra: { prompt?: string; messages?: Array<{ role: string; content: string }> } = {},
): Record<string, unknown> {
  if (!spec.agentId && (typeof spec.systemPrompt !== "string" || spec.systemPrompt.length === 0)) {
    throw new MantyxError("Either `agentId` or `systemPrompt` is required");
  }
  const body: Record<string, unknown> = {
    tools: serializeToolRefs(spec.tools ?? []),
  };
  if (typeof spec.systemPrompt === "string") body.systemPrompt = spec.systemPrompt;
  if (spec.agentId) body.agentId = spec.agentId;
  if (spec.name) body.name = spec.name;
  if (spec.modelId) body.modelId = spec.modelId;
  if (spec.reasoningLevel !== undefined) {
    body.reasoningLevel = normalizeReasoningLevel(spec.reasoningLevel);
  }
  if (spec.outputSchema !== undefined) {
    body.outputSchema = normalizeOutputSchema(spec.outputSchema);
  }
  if (spec.loopDetection !== undefined) {
    body.loopDetection = normalizeLoopDetection(spec.loopDetection);
  }
  if (spec.toolBudgets !== undefined) {
    body.toolBudgets = normalizeToolBudgets(spec.toolBudgets);
  }
  if (spec.budgets) body.budgets = spec.budgets;
  if (spec.metadata && Object.keys(spec.metadata).length > 0) body.metadata = spec.metadata;
  if (extra.prompt !== undefined) body.prompt = extra.prompt;
  if (extra.messages !== undefined) body.messages = extra.messages;
  return body;
}

function serializeToolRefs(tools: ToolRef[]): unknown[] {
  return tools.map((t) => {
    switch (t.kind) {
      case "mantyx":
        return { kind: "mantyx", id: t.id };
      case "mantyx_plugin":
        return { kind: "mantyx_plugin", name: t.name };
      case "local":
        return {
          kind: "local",
          name: t.name,
          description: t.description,
          parameters: toToolParametersWire(t.parameters),
          ...(t.outputSchema !== undefined
            ? { outputSchema: toToolParametersWire(t.outputSchema) }
            : {}),
          ...(t.longRunning ? { longRunning: true } : {}),
        };
      case "a2a":
        return {
          kind: "a2a",
          name: t.name,
          ...(t.description !== undefined ? { description: t.description } : {}),
          agentCardUrl: t.agentCardUrl,
          ...(t.headers ? { headers: { ...t.headers } } : {}),
          ...(t.contextId ? { contextId: t.contextId } : {}),
        };
      case "a2a_local": {
        const card = t._resolvedCard;
        if (!card) {
          throw new MantyxError(
            `defineLocalA2A(${JSON.stringify(t.name)}): agent card has not been resolved yet (was \`runAgent\` / \`createSession\` skipped?)`,
          );
        }
        return {
          kind: "a2a_local",
          name: t.name,
          // The wire ships the resolved A2A Agent Card. Shallow-clone so
          // consumers can mutate the input later without affecting the
          // wire payload.
          agentCard: { ...card },
        };
      }
      case "mcp":
        return {
          kind: "mcp",
          name: t.name,
          url: t.url,
          ...(t.headers ? { headers: { ...t.headers } } : {}),
          ...(t.toolFilter ? { toolFilter: [...t.toolFilter] } : {}),
        };
      case "mcp_local": {
        const resolved = t._resolved;
        if (!resolved) {
          throw new MantyxError(
            `defineLocalMcp(${JSON.stringify(t.name)}): MCP server has not been initialised yet`,
          );
        }
        // The SDK owns naming for `mcp_local` (MANTYX does no prefixing).
        // We auto-prefix each upstream tool name with the server label so
        // the model-facing surface is `<server>_<tool>` — mirroring how
        // MANTYX prefixes for `kind: "mcp"`.
        const tools = resolved.tools.map((tool) => {
          const wire: Record<string, unknown> = {
            name: prefixedMcpToolName(t.name, tool.name),
            inputSchema: tool.inputSchema,
          };
          if (typeof tool.description === "string") wire.description = tool.description;
          if (tool.annotations) wire.annotations = tool.annotations;
          return wire;
        });
        return {
          kind: "mcp_local",
          name: t.name,
          serverInfo: { ...resolved.serverInfo },
          tools,
        };
      }
    }
  });
}

/** Internal registry of client-resolved handlers, indexed by `kind`. */
export interface LocalHandlers {
  /** `kind: "local"` — generic local tools, indexed by tool name. */
  localTools: Map<string, LocalTool>;
  /** `kind: "a2a_local"` — local A2A peers, indexed by tool name. */
  a2aTools: Map<string, LocalA2ATool>;
  /** `kind: "mcp_local"` — local MCP servers, indexed by server name. */
  mcpServers: Map<string, LocalMcpServer>;
}

function collectLocalHandlers(tools: ReadonlyArray<ToolRef>): LocalHandlers {
  const localTools = new Map<string, LocalTool>();
  const a2aTools = new Map<string, LocalA2ATool>();
  const mcpServers = new Map<string, LocalMcpServer>();
  for (const t of tools) {
    if (isLocalTool(t)) {
      localTools.set(t.name, t);
    } else if (isLocalA2ATool(t)) {
      a2aTools.set(t.name, t);
    } else if (isLocalMcpServer(t)) {
      mcpServers.set(t.name, t);
    }
  }
  return { localTools, a2aTools, mcpServers };
}

function describeHandlerName(ev: LocalToolCallEvent): string {
  if (ev.kind === "mcp_local" && ev.mcpServer && ev.mcpToolName) {
    return `${ev.mcpServer}/${ev.mcpToolName}`;
  }
  return ev.name;
}

function normalizeReasoningLevel(level: ReasoningLevel): string | number {
  if (typeof level === "number") {
    if (!Number.isFinite(level) || level < 0 || level > 100) {
      throw new MantyxError(
        `reasoningLevel must be a string anchor or an integer in 0..100, got ${level}`,
      );
    }
    return Math.trunc(level);
  }
  if (level === "off" || level === "low" || level === "medium" || level === "high") {
    return level;
  }
  throw new MantyxError(
    `reasoningLevel must be one of "off" | "low" | "medium" | "high" or a number 0..100, got ${JSON.stringify(level)}`,
  );
}

const OUTPUT_SCHEMA_NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/;
const OUTPUT_SCHEMA_MAX_BYTES = 32 * 1024;

/**
 * Validate an `OutputSchema` value and return the wire-shaped object.
 *
 * Mirrors the server-side `400 invalid_request` checks (name regex, schema
 * shape, ≤ 32 KB serialized) so callers get an early local error instead of
 * a round-trip rejection.
 */
function normalizeOutputSchema(value: OutputSchema): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new MantyxError(
      `outputSchema must be an object of shape { name?, schema }, got ${JSON.stringify(value)}`,
    );
  }
  const out: Record<string, unknown> = {};
  if (value.name !== undefined) {
    if (typeof value.name !== "string" || !OUTPUT_SCHEMA_NAME_RE.test(value.name)) {
      throw new MantyxError(
        `outputSchema.name must match /^[a-zA-Z0-9_-]{1,64}$/, got ${JSON.stringify(value.name)}`,
      );
    }
    out.name = value.name;
  }
  const schema = value.schema;
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) {
    throw new MantyxError(
      `outputSchema.schema must be a non-null JSON object (the JSON Schema root)`,
    );
  }
  out.schema = schema;
  let serialized: string;
  try {
    serialized = JSON.stringify(out);
  } catch (err) {
    throw new MantyxError(
      `outputSchema is not JSON-serialisable: ${(err as Error).message ?? String(err)}`,
    );
  }
  if (serialized.length > OUTPUT_SCHEMA_MAX_BYTES) {
    throw new MantyxError(
      `outputSchema serialised JSON is ${serialized.length} bytes; the server enforces a 32 KB limit`,
    );
  }
  return out;
}

const LOOP_DETECTION_THRESHOLD_MAX = 100;

/**
 * Validate a {@link LoopDetection} (or `false`) value and return the
 * wire-shaped value. Mirrors the server-side `400 invalid_request` checks
 * (thresholds in range, hard cutoff strictly greater than consecutive) so
 * callers see an early local error.
 */
function normalizeLoopDetection(
  value: LoopDetection | false,
): false | Record<string, unknown> {
  if (value === false) return false;
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new MantyxError(
      `loopDetection must be an object or the literal \`false\`, got ${JSON.stringify(value)}`,
    );
  }
  const out: Record<string, unknown> = {};
  if (value.consecutiveThreshold !== undefined) {
    out.consecutiveThreshold = assertThreshold(
      "loopDetection.consecutiveThreshold",
      value.consecutiveThreshold,
      2,
    );
  }
  if (value.hardCutoffThreshold !== undefined) {
    out.hardCutoffThreshold = assertThreshold(
      "loopDetection.hardCutoffThreshold",
      value.hardCutoffThreshold,
      3,
    );
  }
  if (
    typeof out.consecutiveThreshold === "number" &&
    typeof out.hardCutoffThreshold === "number" &&
    out.hardCutoffThreshold <= out.consecutiveThreshold
  ) {
    throw new MantyxError(
      `loopDetection.hardCutoffThreshold (${out.hardCutoffThreshold}) must be strictly greater than loopDetection.consecutiveThreshold (${out.consecutiveThreshold})`,
    );
  }
  return out;
}

function assertThreshold(label: string, value: number, min: number): number {
  if (typeof value !== "number" || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new MantyxError(`${label} must be an integer, got ${JSON.stringify(value)}`);
  }
  if (value < min) {
    throw new MantyxError(`${label} must be >= ${min}, got ${value}`);
  }
  if (value > LOOP_DETECTION_THRESHOLD_MAX) {
    throw new MantyxError(
      `${label} must be <= ${LOOP_DETECTION_THRESHOLD_MAX} (server-enforced), got ${value}`,
    );
  }
  return value;
}

const TOOL_BUDGETS_MAX_ENTRIES = 32;
const TOOL_BUDGET_MAX_NAME_LEN = 120;
const TOOL_BUDGET_MAX_CALLS = 1000;

/**
 * Validate a {@link ToolBudgets} value and return the wire-shaped object.
 * Mirrors the server-side `400 invalid_request` checks (max 32 entries,
 * key length 1..120, `maxCalls` ≥ 0 and ≤ 1000) so callers see an early
 * local error. An empty object is valid and signals "clear the runtime
 * defaults"; pass `undefined` to keep them.
 */
function normalizeToolBudgets(value: ToolBudgets): Record<string, { maxCalls: number }> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new MantyxError(
      `toolBudgets must be an object of shape { [name]: { maxCalls } }, got ${JSON.stringify(value)}`,
    );
  }
  const keys = Object.keys(value);
  if (keys.length > TOOL_BUDGETS_MAX_ENTRIES) {
    throw new MantyxError(
      `toolBudgets has ${keys.length} entries; the server enforces a ${TOOL_BUDGETS_MAX_ENTRIES}-entry limit`,
    );
  }
  const out: Record<string, { maxCalls: number }> = {};
  for (const key of keys) {
    if (typeof key !== "string" || key.length < 1 || key.length > TOOL_BUDGET_MAX_NAME_LEN) {
      throw new MantyxError(
        `toolBudgets keys must be 1..${TOOL_BUDGET_MAX_NAME_LEN}-char strings, got ${JSON.stringify(key)}`,
      );
    }
    const entry = value[key];
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
      throw new MantyxError(
        `toolBudgets[${JSON.stringify(key)}] must be an object { maxCalls }, got ${JSON.stringify(entry)}`,
      );
    }
    const maxCalls = entry.maxCalls;
    if (
      typeof maxCalls !== "number" ||
      !Number.isFinite(maxCalls) ||
      !Number.isInteger(maxCalls) ||
      maxCalls < 0
    ) {
      throw new MantyxError(
        `toolBudgets[${JSON.stringify(key)}].maxCalls must be a non-negative integer, got ${JSON.stringify(maxCalls)}`,
      );
    }
    if (maxCalls > TOOL_BUDGET_MAX_CALLS) {
      throw new MantyxError(
        `toolBudgets[${JSON.stringify(key)}].maxCalls must be <= ${TOOL_BUDGET_MAX_CALLS} (server-enforced), got ${maxCalls}`,
      );
    }
    out[key] = { maxCalls };
  }
  return out;
}

/**
 * Parse the terminal text of a `RunResult` as JSON.
 *
 * When the run was submitted with `outputSchema`, MANTYX (via the LLM
 * provider) guarantees the reply parses as JSON in the *vast* majority of
 * cases. Transient model errors (refusal text, truncation under
 * `max_tokens` pressure, exotic Unicode) can still produce strings that
 * fail to `JSON.parse` in rare edge cases — this helper centralises that
 * brittle step and surfaces a typed {@link MantyxParseError} on failure
 * with the original text preserved on `err.text`.
 *
 * Pass an optional `validator` (zod's `.parse`, an Ajv compiled validator,
 * or any function) to re-validate against your source-of-truth schema. The
 * validator's return value (or thrown error) is forwarded to the caller.
 *
 * @example
 * ```ts
 * import { z } from "zod";
 * import { parseRunOutput } from "@mantyx/sdk";
 *
 * const Schema = z.object({ city: z.string(), temperature_c: z.number() });
 * const result = await client.runAgent({
 *   systemPrompt: "...",
 *   prompt: "What's the weather in SF?",
 *   outputSchema: { name: "weather_report", schema: weatherJsonSchema },
 * });
 * const report = parseRunOutput(result, Schema.parse.bind(Schema));
 * //    ^? { city: string; temperature_c: number }
 * ```
 */
export function parseRunOutput<T = unknown>(
  result: RunResult,
  validator?: (value: unknown) => T,
): T {
  let parsed: unknown;
  try {
    parsed = JSON.parse(result.text);
  } catch (err) {
    throw new MantyxParseError(
      `Run ${result.runId} returned non-JSON text; cannot satisfy outputSchema`,
      result.text,
      { cause: err },
    );
  }
  if (validator) {
    try {
      return validator(parsed);
    } catch (err) {
      throw new MantyxParseError(
        `Run ${result.runId} output failed validation: ${(err as Error).message ?? String(err)}`,
        result.text,
        { cause: err },
      );
    }
  }
  return parsed as T;
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * Defensively coerce a wire `tokens` object into {@link RunTokenUsage}.
 *
 * Returns `undefined` when the input is not a JSON object — that keeps
 * the "no usage data" sentinel intact against legacy MANTYX servers
 * that omit the field entirely. Unknown / missing buckets default to
 * `0` (the protocol contract is that misbehaving engines clamp to
 * non-negative integers; the SDK mirrors that here so dashboards never
 * see `NaN`).
 */
function parseRunTokens(value: unknown): RunTokenUsage | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  return {
    inputTokens: toNonNegativeInt(v.inputTokens),
    cachedTokens: toNonNegativeInt(v.cachedTokens),
    reasoningTokens: toNonNegativeInt(v.reasoningTokens),
    outputTokens: toNonNegativeInt(v.outputTokens),
  };
}

/**
 * Defensively coerce a wire `turns` value into an integer. Returns
 * `undefined` when missing / unparseable — keeps the "no usage data"
 * sentinel against legacy servers.
 */
function parseRunTurns(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  return Math.max(0, Math.trunc(value));
}

/**
 * Defensively coerce a wire `model` object into {@link RunModelInfo}.
 *
 * Returns `undefined` when the input is not a JSON object — the
 * "no usage data" sentinel for legacy servers. `reasoningEffort` is
 * carried through only when the wire surfaced it (the field is
 * optional on the protocol side).
 */
function parseRunModel(value: unknown): RunModelInfo | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  const out: RunModelInfo = {
    id: typeof v.id === "string" ? v.id : "",
    provider: typeof v.provider === "string" ? v.provider : "",
    vendorModelId: typeof v.vendorModelId === "string" ? v.vendorModelId : "",
  };
  if (typeof v.reasoningEffort === "string" && v.reasoningEffort.length > 0) {
    out.reasoningEffort = v.reasoningEffort;
  }
  return out;
}

function toNonNegativeInt(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return 0;
  return Math.max(0, Math.trunc(value));
}

/**
 * Pick exactly one of `apiKey` / `accessToken` / `tokenSource` from
 * {@link MantyxClientOptions} and return the resolved bearer credential
 * (plus the optional dynamic source).
 *
 * `apiKey` and `accessToken` are both static workspace bearers — the
 * server resolves whichever credential it sees by token-prefix, so the
 * SDK can use a single header path. `tokenSource` is the dynamic
 * alternative that the HTTP layer calls before every request and on
 * 401 retries; it is mutually exclusive with the static options
 * because mixing them would obscure where the credential actually
 * came from.
 */
function resolveCredential(opts: MantyxClientOptions): {
  credential: string;
  tokenSource: TokenSource | null;
} {
  const apiKey = typeof opts.apiKey === "string" ? opts.apiKey : "";
  const accessToken = typeof opts.accessToken === "string" ? opts.accessToken : "";
  const tokenSource = typeof opts.tokenSource === "function" ? opts.tokenSource : null;
  const provided = [apiKey ? "apiKey" : "", accessToken ? "accessToken" : "", tokenSource ? "tokenSource" : ""]
    .filter((s) => s.length > 0);
  if (provided.length > 1) {
    throw new MantyxError(
      `Pass exactly one of \`apiKey\`, \`accessToken\`, or \`tokenSource\` — got ${provided.join(" + ")}.`,
    );
  }
  if (provided.length === 0) {
    throw new MantyxError(
      "One of `apiKey` (workspace API key), `accessToken` (OAuth access token), or `tokenSource` (dynamic credential provider) is required",
    );
  }
  return {
    credential: apiKey || accessToken,
    tokenSource,
  };
}

/**
 * Extract the list of scopes the server reported as required for the
 * route, from either the response body's `required` field or the
 * `WWW-Authenticate: Bearer error="insufficient_scope", scope="…"` header.
 *
 * The body field can be a single string (most routes) or an array
 * (multi-scope routes). The header carries a space-delimited scope
 * string per RFC 6750. We prefer the body since it's stricter, and
 * fall back to the header so we surface *something* even when the
 * route only returned the header.
 */
function parseRequiredScopes(
  bodyRequired: string | string[] | undefined,
  wwwAuthenticate: string | null,
): string[] {
  if (Array.isArray(bodyRequired)) {
    return bodyRequired.filter((s): s is string => typeof s === "string" && s.length > 0);
  }
  if (typeof bodyRequired === "string" && bodyRequired.length > 0) {
    return [bodyRequired];
  }
  if (typeof wwwAuthenticate === "string") {
    const m = /scope="([^"]+)"/i.exec(wwwAuthenticate);
    if (m && m[1]) {
      return m[1].split(/\s+/).filter((s) => s.length > 0);
    }
  }
  return [];
}
