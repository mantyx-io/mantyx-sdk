/**
 * Resolution + dispatch for `a2a_local` and `mcp_local` tool refs.
 *
 * The wire protocol requires the SDK to ship the *resolved* A2A Agent Card
 * and the *resolved* MCP `Tool[]` inline as part of the spec. Users only
 * give the SDK a URL (or stdio command); this module turns that into the
 * resolved snapshot at spec-submit time, and speaks A2A `message/send` /
 * MCP `tools/call` at invocation time.
 *
 * Internal — not part of the public API.
 */
import type {
  LocalA2ATool,
  LocalMcpServer,
  McpClientLike,
  ResolvedAgentCard,
  ResolvedMcpServer,
  ToolRef,
} from "./tools.js";
import { isLocalA2ATool, isLocalMcpServer } from "./tools.js";

/**
 * Walks `tools[]` and resolves every `a2a_local` (HTTP fetch of the Agent
 * Card) + `mcp_local` (open transport, run `Initialize` + `tools/list`)
 * ref. Mutates the refs in place to attach the resolved snapshot, so the
 * subsequent `serializeToolRefs` call can read it. Returns the set of
 * resolutions that were created in *this* call (so the run driver can
 * close the MCP transports it opened when the run ends).
 */
export async function resolveLocalRefs(
  tools: ReadonlyArray<ToolRef> | undefined,
  opts: { fetch: typeof fetch } = { fetch: globalThis.fetch },
): Promise<{
  /** MCP servers opened in *this* call — the run driver closes these on completion. */
  newlyOpenedMcp: LocalMcpServer[];
}> {
  if (!tools || tools.length === 0) return { newlyOpenedMcp: [] };
  const newlyOpenedMcp: LocalMcpServer[] = [];

  // Resolve in parallel so a slow card / MCP connection doesn't gate the others.
  const work: Array<Promise<void>> = [];

  for (const t of tools) {
    if (isLocalA2ATool(t)) {
      if (t._resolvedCard) continue;
      work.push(resolveA2A(t, opts.fetch));
    } else if (isLocalMcpServer(t)) {
      if (t._resolved) continue;
      work.push(
        resolveMcp(t).then((resolved) => {
          if (resolved) newlyOpenedMcp.push(t);
        }),
      );
    }
  }

  await Promise.all(work);
  return { newlyOpenedMcp };
}

async function resolveA2A(t: LocalA2ATool, fetchImpl: typeof fetch): Promise<void> {
  const headers = { Accept: "application/json", ...(t.headers ?? {}) };
  const res = await fetchImpl(t.agentCardUrl, { method: "GET", headers });
  if (!res.ok) {
    throw new Error(
      `defineLocalA2A(${JSON.stringify(t.name)}): GET ${t.agentCardUrl} returned ${res.status} ${res.statusText}`,
    );
  }
  const card = (await res.json()) as ResolvedAgentCard;
  if (!card || typeof card !== "object" || typeof card.name !== "string" || !card.name) {
    throw new Error(
      `defineLocalA2A(${JSON.stringify(t.name)}): ${t.agentCardUrl} did not return a valid Agent Card (missing required \`name\` field)`,
    );
  }
  // Mutate the ref so serialize + dispatch can read it.
  (t as { _resolvedCard?: ResolvedAgentCard })._resolvedCard = card;
}

/**
 * Open the MCP transport, run `Initialize` + `tools/list`, and cache the
 * client + snapshot on `t._resolved`. Returns `true` if a new connection
 * was opened (so the caller can register it for cleanup).
 */
async function resolveMcp(t: LocalMcpServer): Promise<boolean> {
  // We import lazily so users who never use mcp_local don't pay the
  // `@modelcontextprotocol/sdk` startup cost.
  const { Client } = await import("@modelcontextprotocol/sdk/client/index.js");
  let transport: { close?: () => Promise<void> | void };
  let connect: (client: InstanceType<typeof Client>) => Promise<void>;
  if (t.http) {
    const { StreamableHTTPClientTransport } = await import(
      "@modelcontextprotocol/sdk/client/streamableHttp.js"
    );
    const httpTransport = new StreamableHTTPClientTransport(new URL(t.http.url), {
      requestInit: t.http.headers ? { headers: t.http.headers } : {},
    });
    transport = httpTransport;
    connect = (c) => c.connect(httpTransport);
  } else if (t.stdio) {
    const { StdioClientTransport } = await import(
      "@modelcontextprotocol/sdk/client/stdio.js"
    );
    const stdioTransport = new StdioClientTransport({
      command: t.stdio.command,
      ...(t.stdio.args ? { args: t.stdio.args } : {}),
      ...(t.stdio.env ? { env: t.stdio.env } : {}),
      ...(t.stdio.cwd ? { cwd: t.stdio.cwd } : {}),
    });
    transport = stdioTransport;
    connect = (c) => c.connect(stdioTransport);
  } else {
    throw new Error(
      `defineLocalMcp(${JSON.stringify(t.name)}): missing transport (no \`url\` or \`command\` was provided)`,
    );
  }

  const client = new Client({ name: "@mantyx/sdk", version: "0.3.0" }, { capabilities: {} });
  try {
    await connect(client);
  } catch (err) {
    throw new Error(
      `defineLocalMcp(${JSON.stringify(t.name)}): failed to connect — ${(err as Error).message}`,
      { cause: err },
    );
  }

  const serverInfo = (client.getServerVersion() ?? { name: t.name }) as {
    name: string;
    version?: string;
  };
  const listed = await client.listTools();
  const tools = listed.tools.map((tool) => {
    const out: Record<string, unknown> = {
      name: tool.name,
      inputSchema: tool.inputSchema as Record<string, unknown>,
    };
    if (typeof tool.description === "string") out.description = tool.description;
    if (tool.annotations) out.annotations = tool.annotations as Record<string, unknown>;
    return out as ResolvedMcpServer["tools"][number];
  });

  const close = async (): Promise<void> => {
    try {
      await client.close();
    } catch {
      /* best effort */
    }
    try {
      const t = transport as { close?: () => Promise<void> | void };
      if (t.close) await t.close();
    } catch {
      /* best effort */
    }
  };

  (t as { _resolved?: ResolvedMcpServer })._resolved = {
    serverInfo,
    tools,
    client: client as unknown as McpClientLike,
    close,
  };
  return true;
}

/**
 * Close every MCP transport on the given refs (best-effort, idempotent).
 * Safe to call multiple times — the resolved cache is cleared after the
 * first call so re-running a session re-opens fresh connections.
 */
export async function closeMcpRefs(tools: ReadonlyArray<ToolRef> | undefined): Promise<void> {
  if (!tools || tools.length === 0) return;
  const closes: Array<Promise<void>> = [];
  for (const t of tools) {
    if (!isLocalMcpServer(t)) continue;
    const resolved = t._resolved;
    if (!resolved) continue;
    (t as { _resolved?: ResolvedMcpServer })._resolved = undefined;
    closes.push(resolved.close());
  }
  await Promise.all(closes);
}

// ---------------------------------------------------- A2A `message/send` call

/**
 * POST a JSON-RPC `message/send` request to the resolved Agent Card's
 * `url`, then extract a single text reply. Returns the empty string when
 * the peer responds with no text content.
 */
export async function callA2A(
  t: LocalA2ATool,
  args: { message: string },
  opts: { fetch: typeof fetch } = { fetch: globalThis.fetch },
): Promise<string> {
  const card = t._resolvedCard;
  if (!card) {
    throw new Error(
      `defineLocalA2A(${JSON.stringify(t.name)}): agent card has not been resolved yet`,
    );
  }
  const url = typeof card.url === "string" && card.url.length > 0 ? card.url : t.agentCardUrl;
  const body = {
    jsonrpc: "2.0" as const,
    id: cryptoRandomId(),
    method: "message/send",
    params: {
      message: {
        kind: "message",
        role: "user",
        messageId: cryptoRandomId(),
        parts: [{ kind: "text", text: args.message }],
      },
    },
  };
  const res = await opts.fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      ...(t.headers ?? {}),
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(
      `A2A message/send to ${url} returned ${res.status} ${res.statusText}`,
    );
  }
  const json = (await res.json()) as {
    result?: unknown;
    error?: { code: number; message: string; data?: unknown };
  };
  if (json.error) {
    throw new Error(`A2A peer reported error ${json.error.code}: ${json.error.message}`);
  }
  return extractA2AReplyText(json.result);
}

/**
 * Pull a single text string out of an A2A `message/send` result. The spec
 * lets the peer reply with either a `Message` (parts[]) or a `Task`. We
 * handle both shapes plus a couple of common fallbacks; anything else is
 * JSON-stringified so the model still gets *something* to reason over.
 */
function extractA2AReplyText(result: unknown): string {
  if (result == null) return "";
  if (typeof result === "string") return result;
  if (typeof result !== "object") return JSON.stringify(result);
  const obj = result as Record<string, unknown>;
  // Message — parts: [{ kind: "text", text: "..." }, ...]
  if (Array.isArray(obj.parts)) {
    const text = textFromParts(obj.parts);
    if (text) return text;
  }
  // Task — status.message.parts (most peers' default reply shape)
  const status = obj.status as Record<string, unknown> | undefined;
  const statusMessage = status?.message as Record<string, unknown> | undefined;
  if (Array.isArray(statusMessage?.parts)) {
    const text = textFromParts(statusMessage!.parts as unknown[]);
    if (text) return text;
  }
  // Task — final artifact text
  const artifacts = obj.artifacts as unknown;
  if (Array.isArray(artifacts) && artifacts.length > 0) {
    const last = artifacts[artifacts.length - 1] as Record<string, unknown>;
    if (Array.isArray(last.parts)) {
      const text = textFromParts(last.parts);
      if (text) return text;
    }
  }
  return JSON.stringify(result);
}

function textFromParts(parts: unknown[]): string {
  const out: string[] = [];
  for (const part of parts) {
    if (!part || typeof part !== "object") continue;
    const p = part as Record<string, unknown>;
    if ((p.kind === "text" || p.type === "text") && typeof p.text === "string") {
      out.push(p.text);
    }
  }
  return out.join("\n");
}

// ----------------------------------------------------------- MCP tools/call

/**
 * Forward a `local_tool_call` for `kind: "mcp_local"` into the cached MCP
 * client's `tools/call` and flatten the response content blocks to a
 * single text string.
 */
export async function callMcpTool(
  server: LocalMcpServer,
  toolName: string,
  args: Record<string, unknown>,
): Promise<string> {
  const resolved = server._resolved;
  if (!resolved) {
    throw new Error(
      `defineLocalMcp(${JSON.stringify(server.name)}): MCP server has not been initialised`,
    );
  }
  const result = await resolved.client.callTool({ name: toolName, arguments: args });
  if (result.isError) {
    const text = textFromMcpContent(result.content) || "MCP tool reported an error";
    throw new Error(text);
  }
  return textFromMcpContent(result.content);
}

function textFromMcpContent(
  content: Array<{ type: string; text?: string; [k: string]: unknown }> | undefined,
): string {
  if (!content || content.length === 0) return "";
  const out: string[] = [];
  for (const block of content) {
    if (block.type === "text" && typeof block.text === "string") out.push(block.text);
  }
  return out.join("\n");
}

// ------------------------------------------------------------------ Helpers

function cryptoRandomId(): string {
  // Best effort: prefer node:crypto / Web Crypto when available, fall back
  // to a Math.random-based id.
  const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
  if (c?.randomUUID) return c.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}
