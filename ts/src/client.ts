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
import { readSseStream } from "./sse.js";
import type { LocalTool, ToolRef } from "./tools.js";
import { isLocalTool } from "./tools.js";
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
  name: string;
  args: Record<string, unknown>;
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
    const handlers = collectLocalHandlers(spec.tools ?? []);
    const created = await this.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: "/agent-runs",
      body: serializeAgentSpec(spec, {
        prompt: spec.prompt,
        messages: spec.messages,
      }),
    });
    return this.driveRun(created.runId, handlers, {
      ...(spec.onAssistantDelta ? { onAssistantDelta: spec.onAssistantDelta } : {}),
      ...(spec.onEvent ? { onEvent: spec.onEvent } : {}),
      ...(spec.signal ? { signal: spec.signal } : {}),
    });
  }

  async *streamAgent(spec: RunSpec): AsyncGenerator<RunEvent, void, void> {
    const handlers = collectLocalHandlers(spec.tools ?? []);
    const created = await this.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: "/agent-runs",
      body: serializeAgentSpec(spec, {
        prompt: spec.prompt,
        messages: spec.messages,
      }),
    });
    yield* this.streamRunEvents(created.runId, handlers, spec.signal);
  }

  // ------------------------------------------------------------- Sessions

  async createSession(spec: SessionSpec): Promise<AgentSession> {
    const handlers = collectLocalHandlers(spec.tools ?? []);
    const created = await this.request<{ sessionId: string; name: string; createdAt: string }>({
      method: "POST",
      path: "/agent-sessions",
      body: serializeAgentSpec(spec),
    });
    return new AgentSession(this, created.sessionId, handlers);
  }

  async resumeSession(
    sessionId: string,
    opts: { tools?: ToolRef[] } = {},
  ): Promise<AgentSession> {
    // Verify the session exists and is still active. Optionally refresh tool defs.
    await this.getSessionInfo(sessionId);
    const handlers = collectLocalHandlers(opts.tools ?? []);
    return new AgentSession(this, sessionId, handlers, opts.tools);
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
    handlers: Map<string, LocalTool>,
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
    handlers: Map<string, LocalTool>,
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
    handlers: Map<string, LocalTool>,
  ): Promise<void> {
    const handler = handlers.get(ev.name);
    if (!handler) {
      await this.postToolResult(runId, ev.toolUseId, {
        error: `No local handler registered for tool ${JSON.stringify(ev.name)}`,
      });
      return;
    }
    try {
      const args = handler.parameters ? handler.parameters.parse?.(ev.args) ?? ev.args : ev.args;
      const out = await handler.execute(args as Record<string, unknown>);
      const resultText = typeof out === "string" ? out : JSON.stringify(out);
      await this.postToolResult(runId, ev.toolUseId, { result: resultText });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      await this.postToolResult(runId, ev.toolUseId, {
        error: new MantyxToolError(handler.name, message).message,
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
  private readonly handlers: Map<string, LocalTool>;
  private readonly toolsForResume: ToolRef[] | undefined;

  constructor(
    client: MantyxClient,
    id: string,
    handlers: Map<string, LocalTool>,
    toolsForResume?: ToolRef[],
  ) {
    this.client = client;
    this.id = id;
    this.handlers = handlers;
    this.toolsForResume = toolsForResume;
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
    } = {},
  ): Promise<RunResult> {
    const created = await this.client.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: `/agent-sessions/${encodeURIComponent(this.id)}/messages`,
      body: {
        prompt,
        ...(this.toolsForResume ? { tools: serializeToolRefs(this.toolsForResume) } : {}),
        ...(opts.metadata && Object.keys(opts.metadata).length > 0
          ? { metadata: opts.metadata }
          : {}),
      },
    });
    return this.client.driveRun(created.runId, this.handlers, {
      ...(opts.onAssistantDelta ? { onAssistantDelta: opts.onAssistantDelta } : {}),
      ...(opts.signal ? { signal: opts.signal } : {}),
    });
  }

  async *stream(
    prompt: string,
    opts: { signal?: AbortSignal; metadata?: Record<string, string> } = {},
  ): AsyncGenerator<RunEvent, void, void> {
    const created = await this.client.request<{ runId: string; streamUrl: string }>({
      method: "POST",
      path: `/agent-sessions/${encodeURIComponent(this.id)}/messages`,
      body: {
        prompt,
        ...(this.toolsForResume ? { tools: serializeToolRefs(this.toolsForResume) } : {}),
        ...(opts.metadata && Object.keys(opts.metadata).length > 0
          ? { metadata: opts.metadata }
          : {}),
      },
    });
    yield* this.client.streamRunEvents(created.runId, this.handlers, opts.signal);
  }

  async history(): Promise<Array<{ role: "user" | "assistant" | "system"; content: string }>> {
    const info = await this.client.getSessionInfo(this.id);
    return info.messages;
  }

  async info(): Promise<SessionInfo> {
    return this.client.getSessionInfo(this.id);
  }

  async end(): Promise<void> {
    await this.client.endSession(this.id);
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
  if (spec.budgets) body.budgets = spec.budgets;
  if (spec.metadata && Object.keys(spec.metadata).length > 0) body.metadata = spec.metadata;
  if (extra.prompt !== undefined) body.prompt = extra.prompt;
  if (extra.messages !== undefined) body.messages = extra.messages;
  return body;
}

function serializeToolRefs(tools: ToolRef[]): unknown[] {
  return tools.map((t) => {
    if (t.kind === "mantyx") return { kind: "mantyx", id: t.id };
    if (t.kind === "mantyx_plugin") return { kind: "mantyx_plugin", name: t.name };
    return {
      kind: "local",
      name: t.name,
      description: t.description,
      parameters: toToolParametersWire(t.parameters),
    };
  });
}

function collectLocalHandlers(tools: ToolRef[]): Map<string, LocalTool> {
  const map = new Map<string, LocalTool>();
  for (const t of tools) {
    if (isLocalTool(t)) map.set(t.name, t);
  }
  return map;
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
