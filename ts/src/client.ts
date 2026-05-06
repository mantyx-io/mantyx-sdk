/**
 * MANTYX SDK client: HTTP plumbing, model catalog, run + session drivers.
 */
import {
  MantyxAuthError,
  MantyxError,
  MantyxNetworkError,
  MantyxRunError,
  MantyxToolError,
} from "./errors.js";
import { callA2A, callMcpTool, closeMcpRefs, resolveLocalRefs } from "./local-resolver.js";
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
  apiKey: string;
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

export interface RunResult {
  runId: string;
  text: string;
  events: RunEvent[];
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
  text: string;
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

export interface ResultEvent extends RunEventBase {
  type: "result";
  subtype: string;
  text?: string;
  error?: string;
}

export interface ErrorEvent extends RunEventBase {
  type: "error";
  error: string;
  code?: string;
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
  readonly options: Required<Pick<MantyxClientOptions, "apiKey" | "workspaceSlug" | "baseUrl">> & {
    fetch: typeof fetch;
    timeoutMs: number;
  };

  constructor(opts: MantyxClientOptions) {
    if (!opts.apiKey || typeof opts.apiKey !== "string") {
      throw new MantyxError("apiKey is required");
    }
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
      apiKey: opts.apiKey,
      workspaceSlug: opts.workspaceSlug,
      baseUrl: (opts.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, ""),
      fetch: f,
      timeoutMs: opts.timeoutMs ?? 60_000,
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
    for await (const ev of this.streamRunEvents(runId, handlers, opts.signal)) {
      collected.push(ev);
      if (opts.onEvent) opts.onEvent(ev);
      if (ev.type === "assistant_delta" && opts.onAssistantDelta) {
        opts.onAssistantDelta((ev as AssistantDeltaEvent).text);
      }
      if (ev.type === "result") {
        const r = ev as ResultEvent;
        if (r.subtype === "success") {
          finalText = typeof r.text === "string" ? r.text : "";
        } else {
          throw new MantyxRunError(runId, r.subtype, r.error ?? r.subtype);
        }
      } else if (ev.type === "error") {
        const e = ev as ErrorEvent;
        throw new MantyxRunError(runId, e.code ?? "error", e.error);
      } else if (ev.type === "cancelled") {
        throw new MantyxRunError(runId, "cancelled", "Run was cancelled");
      }
    }
    return { runId, text: finalText, events: collected };
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
      const res = await this.options.fetch(reqUrl, {
        method: "GET",
        headers: {
          ...this.authHeaders(),
          Accept: "text/event-stream",
          ...(lastSeq > 0 ? { "Last-Event-ID": String(lastSeq) } : {}),
        },
        ...(signal ? { signal } : {}),
      }).catch((err: unknown) => {
        throw new MantyxNetworkError(`Failed to open SSE stream: ${(err as Error).message}`, {
          cause: err,
        });
      });
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

  private authHeaders(): Record<string, string> {
    return { Authorization: `Bearer ${this.options.apiKey}` };
  }

  async request<T>(args: {
    method: string;
    path: string;
    body?: unknown;
    timeoutMs?: number;
  }): Promise<T> {
    const url = this.absoluteUrl(args.path);
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), args.timeoutMs ?? this.options.timeoutMs);
    try {
      const res = await this.options.fetch(url, {
        method: args.method,
        headers: {
          ...this.authHeaders(),
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
    let body: { error?: string; code?: string; hint?: string } = {};
    try {
      body = (await res.json()) as typeof body;
    } catch {
      // ignore
    }
    if (res.status === 401) {
      return new MantyxAuthError(body.error ?? "Invalid API key");
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
    opts: { metadata?: Record<string, string>; reasoningLevel?: ReasoningLevel },
  ): Record<string, unknown> {
    const body: Record<string, unknown> = { prompt };
    if (this.tools.length > 0) body.tools = serializeToolRefs(this.tools);
    if (opts.metadata && Object.keys(opts.metadata).length > 0) body.metadata = opts.metadata;
    if (opts.reasoningLevel !== undefined) {
      body.reasoningLevel = normalizeReasoningLevel(opts.reasoningLevel);
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

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
