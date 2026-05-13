/**
 * Tiny in-process mock of the MANTYX agent-runs HTTP surface, used by the SDK
 * tests. Spins up `node:http` on a random port; supports the subset of
 * endpoints the SDK calls.
 *
 * Each test instantiates a fresh `MockServer`, configures the run/session
 * behaviour, then points a `MantyxClient` at `http://localhost:<port>`.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";

export interface MockToolCallScript {
  toolUseId?: string;
  name: string;
  args: Record<string, unknown>;
}

export interface MockRunScript {
  id?: string;
  /** Sequence of events emitted to the SSE stream (after replay completes). */
  events: Array<MockEvent>;
  /** Optional final text returned in the `result` event. Default: "ok". */
  finalText?: string;
}

export type MockEvent =
  | { type: "assistant_delta"; text: string }
  | {
      type: "assistant_message";
      text: string;
      turn?: number;
      finishReason?: string | null;
      toolCalls?: Array<{ id: string; name: string; input: Record<string, unknown> }>;
    }
  | { type: "tool_result"; name: string; ok?: boolean; summary?: string }
  | {
      type: "local_tool_call";
      toolUseId: string;
      name: string;
      args: Record<string, unknown>;
      /** Discriminator forwarded to the SDK; defaults to `"local"` (omitted on the wire). */
      kind?: "local" | "a2a_local" | "mcp_local";
      /** Echo of the SDK-shipped A2A Agent Card (for `kind: "a2a_local"`). */
      agentCard?: Record<string, unknown>;
      mcpServer?: string;
      mcpToolName?: string;
      /** Echo of the SDK-shipped MCP `Implementation` block (for `kind: "mcp_local"`). */
      mcpServerInfo?: Record<string, unknown>;
      /** When set, hold the SSE stream until the SDK posts a tool result. */
      awaitToolResult?: boolean;
    }
  | { type: "result"; subtype?: string; text?: string }
  | {
      type: "error";
      error: string;
      code?: string;
      errorClass?: string;
      finishReason?: string | null;
      partialText?: string;
      retryable?: boolean;
    }
  | { type: "cancelled"; reason?: string };

interface RunState {
  id: string;
  events: Array<{ seq: number; type: string; data: Record<string, unknown> }>;
  pendingScript: MockEvent[];
  pendingToolResults: Map<string, (payload: { result?: string; error?: string }) => void>;
  notifiers: Set<() => void>;
  done: boolean;
}

export class MockServer {
  private server: Server;
  private runs = new Map<string, RunState>();
  private sessions = new Map<string, { id: string; messages: Array<{ role: string; content: string }> }>();
  /** When set, `POST /agent-runs` returns this script for the next run. */
  scriptForNextRun: MockRunScript | null = null;
  /** When set, `POST /agent-sessions/:id/messages` returns this script for the next run. */
  scriptForNextSessionRun: MockRunScript | null = null;
  /** When true, all routes return 401. */
  failAuth = false;
  /**
   * When > 0, the next N API requests return 401; subsequent calls
   * fall through to normal handling. Used to exercise the SDK's
   * "refresh + retry once on 401" flow without making every request
   * a permanent 401.
   */
  failAuthCount = 0;
  /**
   * When set, all routes return 403 `insufficient_scope` with the
   * configured `required` payload. Single-element arrays serialise as a
   * string (matches the server contract); longer arrays serialise as an
   * array. Tests use this to drive `MantyxScopeError` paths end-to-end.
   */
  failScope: { required: string[] } | null = null;
  /** Auth header captured on the most recent request. */
  lastAuthHeader: string | null = null;
  /** Auth headers across all requests, in arrival order. */
  authHeaderHistory: string[] = [];

  // ── OAuth authorization server simulation ───────────────────────────
  /**
   * Configuration for the mock `POST /api/oauth/token` endpoint.
   * Subsequent test interactions can mutate these between calls.
   */
  oauth = {
    /** Access token returned by the next /token call. */
    accessToken: "mantyx_at_mock_initial",
    /** Refresh token echoed by /token (persistent, non-rotating). */
    refreshToken: "mantyx_rt_mock_initial",
    /** `expires_in` to ship in the response. */
    expiresIn: 3600,
    /** `scope` to ship in the response. */
    scope: "models:read runs:write",
    /**
     * When set, the next /token call returns this error and increments
     * `tokenCallCount` as usual. Cleared after the call.
     */
    nextError: null as { error: string; description?: string; status?: number } | null,
    /** How many distinct access tokens to mint (rolls forward each call). */
    rotateAccessToken: true,
    /** Number of /token requests served. */
    tokenCallCount: 0,
    /** Last form body received on /token. */
    lastTokenRequest: null as Record<string, string> | null,
    /** Number of /revoke requests served. */
    revokeCallCount: 0,
    /** Last form body received on /revoke. */
    lastRevokeRequest: null as Record<string, string> | null,
    /**
     * Optional artificial latency on /token (ms). Lets tests verify
     * single-flight refresh by ensuring concurrent observers race.
     */
    tokenLatencyMs: 0,
  };
  /** Latest body posted to /tool-results endpoints. */
  lastToolResult: { runId: string; payload: Record<string, unknown> } | null = null;
  /** Latest body posted to POST /agent-runs (one-shot create). */
  lastRunCreateBody: Record<string, unknown> | null = null;
  /** Latest body posted to POST /agent-sessions (session create). */
  lastSessionCreateBody: Record<string, unknown> | null = null;
  /** Latest body posted to POST /agent-sessions/:id/messages (turn). */
  lastSessionMessageBody: Record<string, unknown> | null = null;
  /** Override for `GET /a2a/agent-card.json`. Defaults to `defaultMockAgentCard(baseUrl)`. */
  a2aAgentCardResponse: Record<string, unknown> | null = null;
  /** Override for the text returned by `POST /a2a/rpc`. Defaults to "peer reply to: <message>". */
  a2aReplyText: string | null = null;
  /** Latest A2A `message/send` body received. */
  lastA2ARequest: { method: string; message: string; headers: Record<string, unknown> } | null = null;
  models: Array<Record<string, unknown>> = [
    {
      id: "platform:demo",
      label: "Demo Platform",
      provider: "openai",
      vendorModelId: "gpt-test",
      source: "platform_offering",
      contextWindowTokens: 8000,
      pricing: null,
    },
  ];

  port = 0;

  constructor() {
    this.server = createServer((req, res) => {
      void this.handle(req, res).catch((err) => {
        res.statusCode = 500;
        res.end(JSON.stringify({ error: (err as Error).message }));
      });
    });
  }

  async start(): Promise<void> {
    await new Promise<void>((resolve) => {
      this.server.listen(0, "127.0.0.1", () => resolve());
    });
    const addr = this.server.address();
    this.port = typeof addr === "object" && addr ? addr.port : 0;
  }

  async stop(): Promise<void> {
    // Resolve any pending SSE notifiers so the streams close cleanly.
    for (const r of this.runs.values()) {
      r.done = true;
      for (const n of r.notifiers) n();
    }
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
  }

  baseUrl(): string {
    return `http://127.0.0.1:${this.port}`;
  }

  /** Resolve a pending tool-use callback (used by tests that script multi-turn flows). */
  resolveToolUse(runId: string, toolUseId: string, payload: { result?: string; error?: string }): void {
    const run = this.runs.get(runId);
    const cb = run?.pendingToolResults.get(toolUseId);
    if (cb) {
      run!.pendingToolResults.delete(toolUseId);
      cb(payload);
    }
  }

  // --- HTTP routing ----------------------------------------------------

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    this.lastAuthHeader = (req.headers.authorization ?? null) as string | null;
    if (typeof this.lastAuthHeader === "string") {
      this.authHeaderHistory.push(this.lastAuthHeader);
    }
    const url = new URL(req.url ?? "/", this.baseUrl());

    // ── OAuth authorization server simulation ─────────────────────────
    // These endpoints are *not* gated by `failAuth` / `failScope`; they
    // use their own RFC 6749 error model (invalid_grant / invalid_client).
    if (url.pathname === "/api/oauth/token" && req.method === "POST") {
      return this.handleOAuthToken(req, res);
    }
    if (url.pathname === "/api/oauth/revoke" && req.method === "POST") {
      return this.handleOAuthRevoke(req, res);
    }

    if (this.failAuth) {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ error: "Invalid API key or OAuth access token" }));
      return;
    }
    if (this.failAuthCount > 0) {
      this.failAuthCount -= 1;
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ error: "Invalid API key or OAuth access token" }));
      return;
    }
    if (this.failScope) {
      res.statusCode = 403;
      res.setHeader("Content-Type", "application/json");
      const required = this.failScope.required;
      // Match the server's serialisation: string for single-scope routes,
      // array for multi-scope ones.
      const requiredPayload = required.length === 1 ? required[0]! : required;
      res.setHeader(
        "WWW-Authenticate",
        `Bearer error="insufficient_scope", scope="${required.join(" ")}"`,
      );
      res.end(JSON.stringify({ error: "insufficient_scope", required: requiredPayload }));
      return;
    }
    const parts = url.pathname.split("/").filter(Boolean);

    // ── A2A peer simulation routes ──────────────────────────────────────
    // Tests can use these to exercise the URL-only `defineLocalA2A` flow
    // end-to-end without depending on a live A2A peer.
    if (url.pathname === "/a2a/agent-card.json" && req.method === "GET") {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify(this.a2aAgentCardResponse ?? defaultMockAgentCard(this.baseUrl())));
      return;
    }
    if (url.pathname === "/a2a/rpc" && req.method === "POST") {
      const body = (await readJson(req)) as {
        id: number | string;
        method: string;
        params: { message: { parts: Array<{ kind?: string; type?: string; text?: string }> } };
      };
      const text = (body.params?.message?.parts ?? [])
        .map((p) => (typeof p.text === "string" ? p.text : ""))
        .join("\n");
      this.lastA2ARequest = { method: body.method, message: text, headers: req.headers };
      const reply = this.a2aReplyText ?? `peer reply to: ${text}`;
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          jsonrpc: "2.0",
          id: body.id,
          result: {
            kind: "message",
            role: "agent",
            messageId: `m_${randomUUID()}`,
            parts: [{ kind: "text", text: reply }],
          },
        }),
      );
      return;
    }

    // expected: /api/v1/workspaces/<slug>/<rest...>
    if (parts.length < 4 || parts[0] !== "api" || parts[1] !== "v1" || parts[2] !== "workspaces") {
      res.statusCode = 404;
      res.end(JSON.stringify({ error: "Not found" }));
      return;
    }
    const rest = parts.slice(4);
    if (rest[0] === "models" && req.method === "GET") {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ models: this.models, defaultModelId: this.models[0]?.id ?? null }));
      return;
    }
    if (rest[0] === "agent-runs") {
      return this.handleAgentRuns(req, res, rest.slice(1), url);
    }
    if (rest[0] === "agent-sessions") {
      return this.handleAgentSessions(req, res, rest.slice(1));
    }
    res.statusCode = 404;
    res.end(JSON.stringify({ error: "Not found" }));
  }

  private async handleOAuthToken(req: IncomingMessage, res: ServerResponse): Promise<void> {
    this.oauth.tokenCallCount += 1;
    const form = await readForm(req);
    this.oauth.lastTokenRequest = form;
    if (this.oauth.tokenLatencyMs > 0) {
      await new Promise<void>((r) => setTimeout(r, this.oauth.tokenLatencyMs));
    }
    if (this.oauth.nextError) {
      const err = this.oauth.nextError;
      this.oauth.nextError = null;
      res.statusCode = err.status ?? 400;
      res.setHeader("Content-Type", "application/json");
      const payload: Record<string, string> = { error: err.error };
      if (err.description) payload.error_description = err.description;
      res.end(JSON.stringify(payload));
      return;
    }
    const grant = form.grant_type;
    if (grant !== "authorization_code" && grant !== "refresh_token" && grant !== "client_credentials") {
      res.statusCode = 400;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ error: "unsupported_grant_type" }));
      return;
    }
    let accessToken: string;
    if (this.oauth.rotateAccessToken) {
      accessToken = `${this.oauth.accessToken}_v${this.oauth.tokenCallCount}`;
    } else {
      accessToken = this.oauth.accessToken;
    }
    const response: Record<string, unknown> = {
      access_token: accessToken,
      token_type: "Bearer",
      expires_in: this.oauth.expiresIn,
      scope: form.scope ?? this.oauth.scope,
    };
    // refresh_token grant echoes the same value the client just sent
    // (non-rotating per docs/oauth.md). client_credentials never
    // returns one. authorization_code returns the persistent value.
    if (grant === "refresh_token") {
      response.refresh_token = form.refresh_token ?? this.oauth.refreshToken;
    } else if (grant === "authorization_code") {
      response.refresh_token = this.oauth.refreshToken;
    }
    res.statusCode = 200;
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(response));
  }

  private async handleOAuthRevoke(req: IncomingMessage, res: ServerResponse): Promise<void> {
    this.oauth.revokeCallCount += 1;
    this.oauth.lastRevokeRequest = await readForm(req);
    res.statusCode = 200;
    res.setHeader("Content-Type", "application/json");
    res.end("{}");
  }

  private async handleAgentRuns(
    req: IncomingMessage,
    res: ServerResponse,
    rest: string[],
    url: URL,
  ): Promise<void> {
    if (rest.length === 0 && req.method === "POST") {
      const body = (await readJson(req)) as Record<string, unknown>;
      this.lastRunCreateBody = body;
      const id = `run_${randomUUID()}`;
      const script = this.scriptForNextRun ?? { events: [{ type: "result", text: "ok" }] };
      this.scriptForNextRun = null;
      this.startRun(id, script);
      res.statusCode = 202;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ runId: id, streamUrl: `${url.pathname}/${id}/stream` }));
      return;
    }
    if (rest.length === 2 && rest[1] === "stream" && req.method === "GET") {
      const runId = rest[0]!;
      return this.handleSseStream(req, res, runId, url);
    }
    if (rest.length === 2 && rest[1] === "tool-results" && req.method === "POST") {
      const runId = rest[0]!;
      const body = (await readJson(req)) as { toolUseId: string; result?: string; error?: string };
      this.lastToolResult = { runId, payload: body };
      const run = this.runs.get(runId);
      if (run) {
        const cb = run.pendingToolResults.get(body.toolUseId);
        if (cb) {
          run.pendingToolResults.delete(body.toolUseId);
          cb({ result: body.result, error: body.error });
        }
      }
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ ok: true }));
      return;
    }
    if (rest.length === 2 && rest[1] === "cancel" && req.method === "POST") {
      const runId = rest[0]!;
      const run = this.runs.get(runId);
      if (run) {
        run.done = true;
        for (const n of run.notifiers) n();
      }
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ ok: true, status: "cancelled" }));
      return;
    }
    res.statusCode = 404;
    res.end(JSON.stringify({ error: "Not found" }));
  }

  private async handleAgentSessions(
    req: IncomingMessage,
    res: ServerResponse,
    rest: string[],
  ): Promise<void> {
    if (rest.length === 0 && req.method === "POST") {
      const body = (await readJson(req)) as Record<string, unknown>;
      this.lastSessionCreateBody = body;
      const id = `sess_${randomUUID()}`;
      this.sessions.set(id, { id, messages: [] });
      res.statusCode = 201;
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({ sessionId: id, name: "ephemeral", createdAt: new Date().toISOString() }),
      );
      return;
    }
    if (rest.length === 1 && req.method === "GET") {
      const session = this.sessions.get(rest[0]!);
      if (!session) {
        res.statusCode = 404;
        res.end(JSON.stringify({ error: "Session not found" }));
        return;
      }
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          id: session.id,
          name: "ephemeral",
          status: "active",
          createdAt: new Date().toISOString(),
          lastUsedAt: new Date().toISOString(),
          endedAt: null,
          agentSpec: { systemPrompt: "" },
          messages: session.messages,
          metadata: {},
        }),
      );
      return;
    }
    if (rest.length === 1 && req.method === "DELETE") {
      this.sessions.delete(rest[0]!);
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ ok: true }));
      return;
    }
    if (rest.length === 2 && rest[1] === "messages" && req.method === "POST") {
      const sessionId = rest[0]!;
      const session = this.sessions.get(sessionId);
      if (!session) {
        res.statusCode = 404;
        res.end(JSON.stringify({ error: "Session not found" }));
        return;
      }
      const body = (await readJson(req)) as { prompt: string };
      this.lastSessionMessageBody = body as Record<string, unknown>;
      const id = `run_${randomUUID()}`;
      const script = this.scriptForNextSessionRun ?? {
        events: [{ type: "result", text: `echo: ${body.prompt}` }],
      };
      this.scriptForNextSessionRun = null;
      const finalText = lastResultText(script) ?? "";
      session.messages.push({ role: "user", content: body.prompt });
      session.messages.push({ role: "assistant", content: finalText });
      this.startRun(id, script);
      res.statusCode = 202;
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          runId: id,
          streamUrl: `/api/v1/workspaces/x/agent-runs/${id}/stream`,
        }),
      );
      return;
    }
    res.statusCode = 404;
    res.end(JSON.stringify({ error: "Not found" }));
  }

  private startRun(id: string, script: MockRunScript): void {
    const run: RunState = {
      id,
      events: [],
      pendingScript: [...script.events],
      pendingToolResults: new Map(),
      notifiers: new Set(),
      done: false,
    };
    this.runs.set(id, run);
    void this.advanceRun(run, script);
  }

  private async advanceRun(run: RunState, script: MockRunScript): Promise<void> {
    while (run.pendingScript.length > 0) {
      const ev = run.pendingScript.shift()!;
      if (ev.type === "local_tool_call" && ev.awaitToolResult) {
        const { type: _type, awaitToolResult: _await, ...payload } = ev;
        void _type;
        void _await;
        this.appendEvent(run, "local_tool_call", payload as Record<string, unknown>);
        await new Promise<void>((resolve) => {
          run.pendingToolResults.set(ev.toolUseId, () => resolve());
        });
      } else if (ev.type === "local_tool_call") {
        const { type: _type, awaitToolResult: _await, ...payload } = ev;
        void _type;
        void _await;
        this.appendEvent(run, "local_tool_call", payload as Record<string, unknown>);
      } else {
        const { type, ...payload } = ev as { type: string } & Record<string, unknown>;
        this.appendEvent(run, type, payload);
      }
      if (ev.type === "result" || ev.type === "error" || ev.type === "cancelled") {
        run.done = true;
      }
    }
    if (!run.done) {
      this.appendEvent(run, "result", { subtype: "success", text: script.finalText ?? "ok" });
      run.done = true;
    }
    for (const n of run.notifiers) n();
  }

  private appendEvent(run: RunState, type: string, data: Record<string, unknown>): void {
    const seq = run.events.length + 1;
    run.events.push({ seq, type, data: { seq, ...data } });
    for (const n of run.notifiers) n();
  }

  private async handleSseStream(
    req: IncomingMessage,
    res: ServerResponse,
    runId: string,
    url: URL,
  ): Promise<void> {
    const run = this.runs.get(runId);
    if (!run) {
      res.statusCode = 404;
      res.end(JSON.stringify({ error: "Run not found" }));
      return;
    }
    res.statusCode = 200;
    res.setHeader("Content-Type", "text/event-stream; charset=utf-8");
    res.setHeader("Cache-Control", "no-cache, no-transform");
    res.setHeader("Connection", "keep-alive");
    res.flushHeaders();

    const fromSeq = Number(url.searchParams.get("lastSeq") ?? req.headers["last-event-id"] ?? "0") || 0;
    let lastSent = 0;
    const flush = (): void => {
      for (const ev of run.events) {
        if (ev.seq <= fromSeq) {
          lastSent = ev.seq;
          continue;
        }
        if (ev.seq <= lastSent) continue;
        res.write(`id: ${ev.seq}\n`);
        res.write(`event: ${ev.type}\n`);
        res.write(`data: ${JSON.stringify(ev.data)}\n\n`);
        lastSent = ev.seq;
      }
    };

    flush();

    if (run.done) {
      res.end();
      return;
    }

    let waiting = false;
    const wake = (): void => {
      flush();
      if (run.done) {
        res.end();
        run.notifiers.delete(wake);
      }
    };
    run.notifiers.add(wake);
    waiting = true;
    void waiting;

    req.on("close", () => {
      run.notifiers.delete(wake);
      try {
        res.end();
      } catch {
        // ignore
      }
    });
  }
}

function readForm(req: IncomingMessage): Promise<Record<string, string>> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c as Buffer));
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      const out: Record<string, string> = {};
      for (const [k, v] of new URLSearchParams(raw)) {
        out[k] = v;
      }
      resolve(out);
    });
    req.on("error", reject);
  });
}

function readJson(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c as Buffer));
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      if (!raw) return resolve({});
      try {
        resolve(JSON.parse(raw));
      } catch (err) {
        reject(err);
      }
    });
    req.on("error", reject);
  });
}

function defaultMockAgentCard(baseUrl: string): Record<string, unknown> {
  return {
    name: "Acme HR",
    description: "Answers HR policy and PTO questions.",
    url: `${baseUrl}/a2a/rpc`,
    protocolVersion: "0.3.0",
    skills: [
      { id: "pto_lookup", name: "PTO lookup", description: "Find remaining PTO days." },
    ],
  };
}

function lastResultText(script: MockRunScript): string | null {
  for (let i = script.events.length - 1; i >= 0; i--) {
    const ev = script.events[i]!;
    if (ev.type === "result") return ev.text ?? null;
  }
  return script.finalText ?? null;
}
