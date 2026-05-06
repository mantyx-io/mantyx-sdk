/**
 * Expose a MANTYX agent over the [Agent2Agent (A2A)](https://google-a2a.github.io/A2A/)
 * protocol so other agents can talk to it as a peer.
 *
 * This module is loaded from a separate sub-export (`@mantyx/sdk/a2a-server`) so
 * apps that don't need it never pay the bundle cost of the official A2A SDK or
 * Express. To use it, install the optional peer deps:
 *
 *   npm install @a2a-js/sdk express
 *
 * @example
 *   import { MantyxClient } from "@mantyx/sdk";
 *   import { serveAgentOverA2A } from "@mantyx/sdk/a2a-server";
 *
 *   const client = new MantyxClient({
 *     apiKey: process.env.MANTYX_API_KEY!,
 *     workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
 *   });
 *
 *   const server = await serveAgentOverA2A({
 *     client,
 *     port: 4000,
 *     agent: { agentId: "agent_cm6abc123" },
 *     agentCard: {
 *       name: "Acme Support",
 *       description: "Answers billing and account questions.",
 *       protocolVersion: "0.3.0",
 *       version: "1.0.0",
 *       url: "http://localhost:4000",
 *       skills: [{ id: "support", name: "Support", description: "Customer support",
 *                  tags: ["support"] }],
 *       capabilities: { streaming: true, pushNotifications: false },
 *       defaultInputModes: ["text"],
 *       defaultOutputModes: ["text"],
 *     },
 *   });
 *
 *   console.log(`Listening on ${server.url}`);
 */

import type {
  AgentCard,
  Message,
  MessageSendParams,
  Part,
  Task,
  TaskArtifactUpdateEvent,
  TaskStatusUpdateEvent,
} from "@a2a-js/sdk";
import type {
  AgentExecutor,
  ExecutionEventBus,
  RequestContext,
} from "@a2a-js/sdk/server";

import {
  AgentSession,
  type MantyxClient,
  type RunResult,
  type SessionSpec,
  type RunSpec,
} from "./client.js";
import { MantyxError, MantyxRunError } from "./errors.js";
import type { ReasoningLevel, ToolRef } from "./tools.js";

// --------------------------------------------------------------- Public API

/**
 * Description of the MANTYX agent that should answer A2A requests.
 *
 * Mirrors the existing `runAgent` / `createSession` argument shape:
 *   - `agentId` triggers a persisted workspace agent.
 *   - `systemPrompt` (with optional `modelId`, `tools`, …) defines an ephemeral
 *     agent inline.
 *
 * Either `agentId` or `systemPrompt` is required.
 */
export interface MantyxAgentSpec {
  /** Reference to a persisted MANTYX agent. Mutually exclusive with `systemPrompt`. */
  agentId?: string;
  /** System prompt for an inline / ephemeral agent. Mutually exclusive with `agentId`. */
  systemPrompt?: string;
  modelId?: string;
  tools?: ToolRef[];
  reasoningLevel?: ReasoningLevel;
  metadata?: Record<string, string>;
  budgets?: { maxToolTurns?: number };
  /**
   * Optional human-readable display name for runs created against MANTYX.
   * Visible in the dashboard. Has no effect on the A2A side.
   */
  name?: string;
}

export interface MantyxAgentExecutorOptions {
  client: MantyxClient;
  agent: MantyxAgentSpec;
  /**
   * How to map an incoming A2A `contextId` onto a MANTYX session.
   *
   * - `"auto"` (default): each unique `contextId` opens a MANTYX session on
   *   first contact and reuses it for subsequent messages with the same
   *   `contextId`. Gives you multi-turn out of the box.
   * - `"stateless"`: every A2A message becomes an independent `runAgent`. No
   *   conversational memory; simpler resource model.
   */
  conversation?: "auto" | "stateless";
  /**
   * LRU cap on the in-memory `contextId -> AgentSession` table. When the cap
   * is exceeded the oldest session is `end()`-ed and evicted. Default: 1024.
   * Only consulted when `conversation: "auto"`.
   */
  maxSessions?: number;
  /**
   * Receives streaming MANTYX `assistant_delta` text. The default behaviour
   * forwards every delta as a `TaskStatusUpdateEvent` (state: "working")
   * containing the delta as a `text` part — this is what enables A2A
   * `message/stream` clients to see real-time tokens. Override only if you
   * need to swallow them or transform the wire shape.
   */
  onAssistantDelta?: (delta: string, ctx: RequestContext, eventBus: ExecutionEventBus) => void;
}

export interface ServeAgentOverA2AOptions extends MantyxAgentExecutorOptions {
  /** A2A Agent Card published at `/.well-known/agent-card.json`. */
  agentCard: AgentCard;
  /** TCP port to listen on. Default: 0 (let the OS pick). */
  port?: number;
  /** Bind address. Default: `"0.0.0.0"`. */
  host?: string;
  /** Path that serves the Agent Card JSON. Default: `"/.well-known/agent-card.json"`. */
  agentCardPath?: string;
  /** Path that serves the JSON-RPC endpoint. Default: `"/"`. */
  jsonRpcPath?: string;
  /**
   * Path that serves the HTTP+JSON/REST endpoint. Default: `"/v1"`.
   * Set to `false` to disable the REST mount entirely.
   */
  restPath?: string | false;
}

export interface ServeAgentOverA2AHandle {
  /** Origin of the running server, e.g. `"http://localhost:4000"`. */
  url: string;
  /** Resolved port number (useful when you let the OS pick one). */
  port: number;
  /** Stop the HTTP server, end every cached MANTYX session, and free MCP transports. */
  close: () => Promise<void>;
}

// --------------------------------------------------------- Implementation

/**
 * Implementation of `@a2a-js/sdk`'s `AgentExecutor` that backs a MANTYX agent.
 *
 * Most callers want `serveAgentOverA2A` instead; reach for this class directly
 * when you need to mount the executor inside an existing Express, Fastify, or
 * Connect app.
 */
export class MantyxAgentExecutor implements AgentExecutor {
  readonly client: MantyxClient;
  readonly agent: MantyxAgentSpec;
  readonly conversation: "auto" | "stateless";
  readonly maxSessions: number;
  readonly onAssistantDelta?: MantyxAgentExecutorOptions["onAssistantDelta"];

  /** contextId -> live MANTYX session. Maintained as an LRU map. */
  private readonly sessions = new Map<string, AgentSession>();
  /** taskIds we've been asked to cancel; checked between turns. */
  private readonly cancelled = new Set<string>();
  /** Pending AbortControllers per task, used for cooperative cancel. */
  private readonly inFlight = new Map<string, AbortController>();

  constructor(options: MantyxAgentExecutorOptions) {
    if (!options.client) {
      throw new MantyxError("MantyxAgentExecutor: `client` is required");
    }
    validateAgentSpec(options.agent);
    this.client = options.client;
    this.agent = options.agent;
    this.conversation = options.conversation ?? "auto";
    this.maxSessions = options.maxSessions ?? 1024;
    if (options.onAssistantDelta) this.onAssistantDelta = options.onAssistantDelta;
  }

  async execute(requestContext: RequestContext, eventBus: ExecutionEventBus): Promise<void> {
    const { userMessage, taskId, contextId, task } = requestContext;
    const userText = extractText(userMessage);

    const abort = new AbortController();
    this.inFlight.set(taskId, abort);

    try {
      // Publish initial Task object on the first turn so streaming clients see
      // a stable id; reusing an existing task otherwise.
      if (!task) {
        eventBus.publish({
          kind: "task",
          id: taskId,
          contextId,
          status: { state: "submitted", timestamp: new Date().toISOString() },
          history: [userMessage],
        } satisfies Task);
      }

      eventBus.publish(statusUpdate(taskId, contextId, "working", false));

      if (this.cancelled.has(taskId)) {
        eventBus.publish(statusUpdate(taskId, contextId, "canceled", true));
        eventBus.finished();
        return;
      }

      const onDelta = (delta: string) => {
        if (this.onAssistantDelta) {
          this.onAssistantDelta(delta, requestContext, eventBus);
          return;
        }
        eventBus.publish(deltaStatusUpdate(taskId, contextId, delta));
      };

      let result: RunResult;
      try {
        result = await this.runOnce(contextId, userText, onDelta, abort.signal);
      } catch (err) {
        eventBus.publish(
          completedStatusUpdate(
            taskId,
            contextId,
            this.cancelled.has(taskId) ? "canceled" : "failed",
            errorText(err),
          ),
        );
        eventBus.finished();
        return;
      }

      eventBus.publish(completedStatusUpdate(taskId, contextId, "completed", result.text ?? ""));
      eventBus.finished();
    } finally {
      this.inFlight.delete(taskId);
      this.cancelled.delete(taskId);
    }
  }

  async cancelTask(taskId: string, eventBus: ExecutionEventBus): Promise<void> {
    this.cancelled.add(taskId);
    const ctrl = this.inFlight.get(taskId);
    if (ctrl) ctrl.abort();
    // The active `execute()` call publishes the final 'canceled' status
    // itself; we only need to mark the intent here.
    void eventBus;
  }

  /**
   * Close every cached session. Idempotent. Safe to call from server shutdown
   * paths.
   */
  async close(): Promise<void> {
    const sessions = Array.from(this.sessions.values());
    this.sessions.clear();
    await Promise.allSettled(sessions.map((s) => s.end()));
  }

  // -------------------------------------------------- private session helpers

  private async runOnce(
    contextId: string,
    prompt: string,
    onAssistantDelta: (delta: string) => void,
    signal: AbortSignal,
  ): Promise<RunResult> {
    if (this.conversation === "stateless") {
      const runSpec: RunSpec = {
        ...specForRun(this.agent),
        prompt,
        onAssistantDelta,
        signal,
      };
      return this.client.runAgent(runSpec);
    }

    const session = await this.getOrCreateSession(contextId);
    return session.send(prompt, { onAssistantDelta, signal });
  }

  private async getOrCreateSession(contextId: string): Promise<AgentSession> {
    const existing = this.sessions.get(contextId);
    if (existing) {
      // LRU: bump to most-recently-used.
      this.sessions.delete(contextId);
      this.sessions.set(contextId, existing);
      return existing;
    }
    const sessionSpec: SessionSpec = specForSession(this.agent, contextId);
    const session = await this.client.createSession(sessionSpec);
    this.sessions.set(contextId, session);
    await this.evictIfNeeded();
    return session;
  }

  private async evictIfNeeded(): Promise<void> {
    while (this.sessions.size > this.maxSessions) {
      const oldestKey = this.sessions.keys().next().value as string | undefined;
      if (!oldestKey) break;
      const oldest = this.sessions.get(oldestKey)!;
      this.sessions.delete(oldestKey);
      try {
        await oldest.end();
      } catch {
        // Eviction is best-effort; swallow errors so the next request still works.
      }
    }
  }
}

/**
 * Spin up a small HTTP server that exposes a MANTYX agent as an A2A peer.
 * Mounts the Agent Card, JSON-RPC, and (optionally) REST endpoints from the
 * official `@a2a-js/sdk` library.
 *
 * Throws if `express` / `@a2a-js/sdk` aren't installed; install them as peer
 * deps with `npm install express @a2a-js/sdk`.
 */
export async function serveAgentOverA2A(
  options: ServeAgentOverA2AOptions,
): Promise<ServeAgentOverA2AHandle> {
  const a2a = await loadServerSdk();
  const expressMod = await loadExpress();

  const executor = new MantyxAgentExecutor(options);
  const requestHandler = new a2a.DefaultRequestHandler(
    options.agentCard,
    new a2a.InMemoryTaskStore(),
    executor,
  );

  const app = expressMod();
  app.use(expressMod.json());

  const cardPath = options.agentCardPath ?? "/.well-known/agent-card.json";
  const jsonRpcPath = options.jsonRpcPath ?? "/";
  const restPath = options.restPath === undefined ? "/v1" : options.restPath;

  app.use(
    cardPath,
    a2a.expressApp.agentCardHandler({ agentCardProvider: requestHandler }),
  );
  if (restPath !== false) {
    app.use(
      restPath,
      a2a.expressApp.restHandler({
        requestHandler,
        userBuilder: a2a.expressApp.UserBuilder.noAuthentication,
      }),
    );
  }
  // Mount JSON-RPC last so it doesn't shadow the well-known and REST paths.
  app.use(
    jsonRpcPath,
    a2a.expressApp.jsonRpcHandler({
      requestHandler,
      userBuilder: a2a.expressApp.UserBuilder.noAuthentication,
    }),
  );

  const port = options.port ?? 0;
  const host = options.host ?? "0.0.0.0";
  const server = app.listen(port, host);

  await new Promise<void>((resolve, reject) => {
    server.once("listening", resolve);
    server.once("error", reject);
  });

  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new MantyxError("serveAgentOverA2A: failed to bind HTTP listener");
  }

  return {
    port: address.port,
    url: `http://${displayHost(host)}:${address.port}`,
    close: async () => {
      await new Promise<void>((resolve, reject) =>
        server.close((err) => (err ? reject(err) : resolve())),
      );
      await executor.close();
    },
  };
}

// ----------------------------------------------------------- A2A event helpers

function statusUpdate(
  taskId: string,
  contextId: string,
  state: "submitted" | "working" | "completed" | "canceled" | "failed",
  final: boolean,
): TaskStatusUpdateEvent {
  return {
    kind: "status-update",
    taskId,
    contextId,
    status: { state, timestamp: new Date().toISOString() },
    final,
  };
}

function deltaStatusUpdate(
  taskId: string,
  contextId: string,
  delta: string,
): TaskStatusUpdateEvent {
  return {
    kind: "status-update",
    taskId,
    contextId,
    status: {
      state: "working",
      timestamp: new Date().toISOString(),
      message: {
        kind: "message",
        messageId: randomMessageId(),
        role: "agent",
        parts: [{ kind: "text", text: delta }],
        contextId,
        taskId,
      },
    },
    final: false,
  };
}

function completedStatusUpdate(
  taskId: string,
  contextId: string,
  state: "completed" | "canceled" | "failed",
  text: string,
): TaskStatusUpdateEvent {
  return {
    kind: "status-update",
    taskId,
    contextId,
    status: {
      state,
      timestamp: new Date().toISOString(),
      message: {
        kind: "message",
        messageId: randomMessageId(),
        role: "agent",
        parts: [{ kind: "text", text }],
        contextId,
        taskId,
      },
    },
    final: true,
  };
}

// --------------------------------------------------------- Utility helpers

function extractText(message: Message | undefined): string {
  if (!message) return "";
  const parts = (message.parts as Part[] | undefined) ?? [];
  const out: string[] = [];
  for (const p of parts) {
    if ((p as { kind: string }).kind === "text") {
      const t = (p as { text?: unknown }).text;
      if (typeof t === "string") out.push(t);
    }
  }
  return out.join("\n");
}

function specForRun(spec: MantyxAgentSpec): RunSpec {
  const out: RunSpec = {};
  if (spec.agentId) out.agentId = spec.agentId;
  if (spec.systemPrompt) out.systemPrompt = spec.systemPrompt;
  if (spec.modelId) out.modelId = spec.modelId;
  if (spec.tools) out.tools = spec.tools;
  if (spec.reasoningLevel !== undefined) out.reasoningLevel = spec.reasoningLevel;
  if (spec.metadata) out.metadata = spec.metadata;
  if (spec.budgets) out.budgets = spec.budgets;
  if (spec.name) out.name = spec.name;
  return out;
}

function specForSession(spec: MantyxAgentSpec, contextId: string): SessionSpec {
  const out: SessionSpec = {};
  if (spec.agentId) out.agentId = spec.agentId;
  if (spec.systemPrompt) out.systemPrompt = spec.systemPrompt;
  if (spec.modelId) out.modelId = spec.modelId;
  if (spec.tools) out.tools = spec.tools;
  if (spec.reasoningLevel !== undefined) out.reasoningLevel = spec.reasoningLevel;
  // Tag the session with the originating A2A contextId so it's filterable
  // in the MANTYX dashboard.
  const meta: Record<string, string> = { ...(spec.metadata ?? {}) };
  if (!meta.a2a_context_id) meta.a2a_context_id = contextId;
  out.metadata = meta;
  if (spec.budgets) out.budgets = spec.budgets;
  if (spec.name) out.name = spec.name;
  return out;
}

function validateAgentSpec(spec: MantyxAgentSpec): void {
  if (!spec.agentId && (!spec.systemPrompt || spec.systemPrompt.length === 0)) {
    throw new MantyxError(
      "MantyxAgentExecutor: `agent.agentId` or `agent.systemPrompt` is required",
    );
  }
}

function errorText(err: unknown): string {
  if (err instanceof MantyxRunError) {
    return `MANTYX run failed (${err.subtype ?? "unknown"}): ${err.message}`;
  }
  if (err instanceof Error) return err.message;
  try {
    return String(err);
  } catch {
    return "unknown error";
  }
}

function randomMessageId(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  // Fallback: timestamp + random suffix; A2A only requires uniqueness.
  return `msg_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
}

function displayHost(host: string): string {
  if (host === "0.0.0.0" || host === "::") return "localhost";
  return host;
}

// --------------------------------------------------- Optional-dep loaders

interface ExpressLoader {
  (): import("express").Express;
  json(): import("express").RequestHandler;
}

interface A2AServerSdk {
  DefaultRequestHandler: typeof import("@a2a-js/sdk/server").DefaultRequestHandler;
  InMemoryTaskStore: typeof import("@a2a-js/sdk/server").InMemoryTaskStore;
  expressApp: typeof import("@a2a-js/sdk/server/express");
}

async function loadExpress(): Promise<ExpressLoader> {
  try {
    const mod = (await import("express")) as unknown as
      | ExpressLoader
      | { default: ExpressLoader };
    return "default" in mod ? mod.default : mod;
  } catch (err) {
    throw new MantyxError(
      "serveAgentOverA2A: `express` is required but not installed. Run `npm install express @a2a-js/sdk` to enable the A2A server.",
    );
  }
}

async function loadServerSdk(): Promise<A2AServerSdk> {
  let server: typeof import("@a2a-js/sdk/server");
  let express: typeof import("@a2a-js/sdk/server/express");
  try {
    server = (await import("@a2a-js/sdk/server")) as typeof import("@a2a-js/sdk/server");
  } catch (err) {
    throw new MantyxError(
      "serveAgentOverA2A: `@a2a-js/sdk` is required but not installed. Run `npm install @a2a-js/sdk express` to enable the A2A server.",
    );
  }
  try {
    express = (await import(
      "@a2a-js/sdk/server/express"
    )) as typeof import("@a2a-js/sdk/server/express");
  } catch (err) {
    throw new MantyxError(
      "serveAgentOverA2A: `@a2a-js/sdk/server/express` could not be loaded; ensure the installed `@a2a-js/sdk` is at least v0.3.",
    );
  }
  return {
    DefaultRequestHandler: server.DefaultRequestHandler,
    InMemoryTaskStore: server.InMemoryTaskStore,
    expressApp: express,
  };
}

// Re-export for callers that just want to compose the executor with their own
// transport stack (e.g. plug it into Fastify or Cloudflare Workers).
export type {
  AgentCard,
  Message,
  MessageSendParams,
  Task,
  TaskArtifactUpdateEvent,
  TaskStatusUpdateEvent,
} from "@a2a-js/sdk";
export type { AgentExecutor, ExecutionEventBus, RequestContext } from "@a2a-js/sdk/server";
