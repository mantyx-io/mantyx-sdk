/**
 * Public tool helpers for the MANTYX SDK.
 *
 * Server-resolved (executed by MANTYX):
 *   mantyxTool(id)            → existing workspace `Tool` row by id
 *   mantyxPluginTool(name)    → built-in plugin tool by `@plugin/tool` name
 *   mantyxA2A({...})          → remote Agent2Agent peer, dialed by MANTYX
 *   mantyxMcp({...})          → remote MCP server (Streamable HTTP), proxied by MANTYX
 *
 * Client-resolved (executed in this process; the SDK shuttles inputs and
 * outputs over the agent loop):
 *   defineLocalTool({...})    → generic local tool with a Zod parameter schema
 *   defineLocalA2A({...})     → A2A peer the SDK can reach but MANTYX cannot —
 *                                pass an `agentCardUrl`; the SDK fetches the
 *                                Agent Card and speaks A2A `message/send` for you.
 *   defineLocalMcp({...})     → MCP server the SDK manages — pass either a
 *                                Streamable HTTP `url` or a stdio
 *                                `command`; the SDK runs `Initialize` +
 *                                `tools/list` and forwards `tools/call` for you.
 *
 * The server emits a `local_tool_call` event for every client-resolved
 * invocation. The event carries a `kind` discriminator (`"local"` is implied
 * when omitted, `"a2a_local"` and `"mcp_local"` are explicit) so the SDK can
 * dispatch to the right handler.
 */
import type { z } from "zod";

export type ZodLikeObject = z.ZodType<Record<string, unknown>> & {
  _def?: unknown;
  parse?: (value: unknown) => unknown;
};

/**
 * Provider-thinking knob, mapped server-side onto each LLM's native dial:
 * `reasoning.effort` (OpenAI), `thinkingConfig.thinkingLevel` / `thinkingBudget`
 * (Gemini), or extended-thinking budget (Anthropic / Bedrock-Anthropic).
 *
 * Pass either a string anchor (`"off" | "low" | "medium" | "high"`) or an
 * integer in `0..100` (where `0` explicitly disables provider thinking).
 */
export type ReasoningLevel = "off" | "low" | "medium" | "high" | number;

// ---------------------------------------------------------- Generic local tool

export interface LocalTool<TArgs = Record<string, unknown>> {
  readonly kind: "local";
  readonly name: string;
  readonly description: string;
  readonly parameters: ZodLikeObject | undefined;
  readonly execute: (args: TArgs) => Promise<string> | string;
}

export interface DefineLocalToolOptions<T extends ZodLikeObject | undefined> {
  /** Lowercase alphanumeric + underscore, max 64 chars. */
  name: string;
  description?: string;
  parameters?: T;
  execute: (
    args: T extends ZodLikeObject ? z.infer<T> : Record<string, unknown>,
  ) => Promise<string> | string;
}

export function defineLocalTool<T extends ZodLikeObject | undefined>(
  opts: DefineLocalToolOptions<T>,
): LocalTool {
  assertToolName(opts.name);
  return {
    kind: "local",
    name: opts.name,
    description: opts.description ?? "",
    parameters: opts.parameters,
    execute: opts.execute as LocalTool["execute"],
  };
}

// ------------------------------------------------------------ Server-resolved

export interface MantyxToolRef {
  readonly kind: "mantyx";
  readonly id: string;
}

export interface MantyxPluginToolRef {
  readonly kind: "mantyx_plugin";
  readonly name: string;
}

export function mantyxTool(id: string): MantyxToolRef {
  if (typeof id !== "string" || id.length === 0) {
    throw new Error("mantyxTool(id): id must be a non-empty string");
  }
  return { kind: "mantyx", id };
}

export function mantyxPluginTool(name: string): MantyxPluginToolRef {
  if (typeof name !== "string" || !name.startsWith("@") || !name.includes("/")) {
    throw new Error(
      `mantyxPluginTool(name): expected "@plugin-slug/tool-name", got ${JSON.stringify(name)}`,
    );
  }
  return { kind: "mantyx_plugin", name };
}

// ------------------------------------------------------------------------ A2A

/**
 * Reference to a remote Agent2Agent peer reachable from MANTYX (server-resolved).
 * MANTYX dials `agentCardUrl` over A2A's `message/send` RPC and forwards the
 * remote agent's reply as the tool result.
 */
export interface A2AToolRef {
  readonly kind: "a2a";
  readonly name: string;
  readonly description?: string;
  readonly agentCardUrl: string;
  readonly headers?: Record<string, string>;
  readonly contextId?: string;
}

export interface MantyxA2AOptions {
  /** Tool name surfaced to the model; must match `^[a-zA-Z0-9_]{1,64}$`. */
  name: string;
  description?: string;
  /** Remote Agent Card URL (`/.well-known/agent-card.json`) or JSON-RPC root. */
  agentCardUrl: string;
  /** Per-request HTTP headers (typically `Authorization`). */
  headers?: Record<string, string>;
  /** Optional A2A `contextId` to thread multiple delegations. */
  contextId?: string;
}

export function mantyxA2A(opts: MantyxA2AOptions): A2AToolRef {
  assertToolName(opts.name);
  if (typeof opts.agentCardUrl !== "string" || opts.agentCardUrl.length === 0) {
    throw new Error("mantyxA2A: agentCardUrl is required");
  }
  return {
    kind: "a2a",
    name: opts.name,
    ...(opts.description !== undefined ? { description: opts.description } : {}),
    agentCardUrl: opts.agentCardUrl,
    ...(opts.headers ? { headers: { ...opts.headers } } : {}),
    ...(opts.contextId ? { contextId: opts.contextId } : {}),
  };
}

/**
 * Local A2A peer — the SDK fetches the Agent Card from `agentCardUrl` on
 * the first run, ships the resolved card with the spec, and speaks A2A
 * `message/send` to `agentCard.url` whenever MANTYX emits a
 * `local_tool_call` for this tool. You only supply the URL.
 *
 * The model addresses this tool by `name` and always passes
 * `{ message: string }` as arguments.
 *
 * Resolution is lazy: the card is fetched on the first `runAgent` /
 * `streamAgent` / `createSession` (or `session.send`) call and cached on
 * the tool ref for the rest of the process lifetime. Re-construct the ref
 * to force a refetch.
 */
export interface LocalA2ATool {
  readonly kind: "a2a_local";
  readonly name: string;
  /** Where the SDK fetches the Agent Card from. Cached after the first hit. */
  readonly agentCardUrl: string;
  /**
   * Headers used for **both** the card fetch and every `message/send` POST
   * (typically `Authorization` for intranet peers).
   */
  readonly headers: Record<string, string> | undefined;
  /**
   * Internal cache of the resolved Agent Card. Populated lazily by the
   * client on the first run; not part of the user contract.
   * @internal
   */
  _resolvedCard?: ResolvedAgentCard;
}

/** Internal: the snapshot of a resolved A2A Agent Card. */
export interface ResolvedAgentCard {
  /** Required by the A2A spec; used for the synthesized tool description. */
  readonly name: string;
  /** Peer's A2A endpoint — where the SDK POSTs `message/send`. */
  readonly url?: string;
  /** Anything else the peer publishes; forwarded verbatim onto the wire. */
  readonly [k: string]: unknown;
}

export interface DefineLocalA2AOptions {
  /** Tool name surfaced to the model. Must match `^[a-zA-Z0-9_]{1,64}$`. */
  name: string;
  /**
   * URL of the peer's Agent Card (`/.well-known/agent-card.json` is the
   * conventional path). The SDK fetches this on the first run, parses it,
   * and ships the resolved card with the spec. The resolved card's `url`
   * field is then used as the `message/send` target.
   */
  agentCardUrl: string;
  /**
   * Headers attached to **both** the card fetch and every `message/send`
   * POST (typically `Authorization` for intranet peers).
   */
  headers?: Record<string, string>;
}

export function defineLocalA2A(opts: DefineLocalA2AOptions): LocalA2ATool {
  assertToolName(opts.name);
  if (typeof opts.agentCardUrl !== "string" || opts.agentCardUrl.length === 0) {
    throw new Error("defineLocalA2A: `agentCardUrl` is required");
  }
  return {
    kind: "a2a_local",
    name: opts.name,
    agentCardUrl: opts.agentCardUrl,
    headers: opts.headers ? { ...opts.headers } : undefined,
  };
}

// ------------------------------------------------------------------------ MCP

/**
 * Reference to a remote MCP server (Streamable HTTP) discovered + proxied by
 * MANTYX. Each tool in the catalog is exposed to the model as `<name>_<tool>`.
 */
export interface McpToolRef {
  readonly kind: "mcp";
  readonly name: string;
  readonly url: string;
  readonly headers?: Record<string, string>;
  readonly toolFilter?: string[];
}

export interface MantyxMcpOptions {
  /** Server label; used to prefix every discovered tool. */
  name: string;
  /** Streamable HTTP MCP endpoint. */
  url: string;
  headers?: Record<string, string>;
  /** Optional allowlist of MCP tool names. */
  toolFilter?: string[];
}

export function mantyxMcp(opts: MantyxMcpOptions): McpToolRef {
  assertToolName(opts.name);
  if (typeof opts.url !== "string" || opts.url.length === 0) {
    throw new Error("mantyxMcp: url is required");
  }
  return {
    kind: "mcp",
    name: opts.name,
    url: opts.url,
    ...(opts.headers ? { headers: { ...opts.headers } } : {}),
    ...(opts.toolFilter ? { toolFilter: [...opts.toolFilter] } : {}),
  };
}

/**
 * Streamable HTTP transport spec for {@link defineLocalMcp}.
 */
export interface LocalMcpHttpTransport {
  /** Streamable HTTP MCP endpoint (e.g. `http://localhost:8080/mcp`). */
  url: string;
  /** HTTP headers sent on every MCP request (typically `Authorization`). */
  headers?: Record<string, string>;
}

/**
 * stdio transport spec for {@link defineLocalMcp}. The SDK spawns the
 * specified executable with the given arguments and speaks JSON-RPC over
 * its stdin/stdout streams.
 */
export interface LocalMcpStdioTransport {
  /** Executable to launch (e.g. `mcp-server-filesystem`, `node`, `uvx`). */
  command: string;
  /** Arguments passed verbatim. */
  args?: string[];
  /** Environment variables for the child process. Inherited if undefined. */
  env?: Record<string, string>;
  /** Working directory for the child process. */
  cwd?: string;
}

/**
 * Local MCP server — the SDK manages the entire MCP lifecycle for you.
 *
 * Pass either a Streamable HTTP `url` or an stdio `command` (mutually
 * exclusive). On the first run, the SDK opens the transport, runs MCP's
 * `Initialize` (capturing the `Implementation` block) and `tools/list`
 * (capturing the catalog), and ships both inline as part of the spec. On
 * every `local_tool_call` with `kind: "mcp_local"` the SDK forwards the
 * call to MCP `tools/call` on the cached connection and POSTs the
 * flattened text response back to MANTYX.
 *
 * Connections are reused across runs and across messages within a session
 * and closed on `runAgent` completion / `session.end()`.
 */
export interface LocalMcpServer {
  readonly kind: "mcp_local";
  readonly name: string;
  /** One-of with {@link stdio} — the transport spec is mutually exclusive. */
  readonly http: LocalMcpHttpTransport | undefined;
  /** One-of with {@link http} — the transport spec is mutually exclusive. */
  readonly stdio: LocalMcpStdioTransport | undefined;
  /**
   * Internal cache of the resolved MCP client + catalog. Populated lazily
   * by the run driver on the first run; not part of the user contract.
   * @internal
   */
  _resolved?: ResolvedMcpServer;
}

/**
 * Internal: the live MCP client + the snapshot the SDK ships on the wire.
 */
export interface ResolvedMcpServer {
  /** The MCP `Implementation` block from `Initialize`. */
  readonly serverInfo: { name: string; version?: string; [k: string]: unknown };
  /** Verbatim `tools/list` output. */
  readonly tools: ReadonlyArray<{
    readonly name: string;
    readonly description?: string;
    readonly inputSchema: Record<string, unknown>;
    readonly annotations?: Record<string, unknown>;
    readonly [k: string]: unknown;
  }>;
  /**
   * The live MCP client. Used by the run driver to call `tools/call`. The
   * concrete type depends on the transport — kept loose so this header
   * file doesn't have to import the MCP SDK type.
   */
  readonly client: McpClientLike;
  /** Closes the MCP transport (idempotent). */
  readonly close: () => Promise<void>;
}

/** Minimal interface this SDK uses against an MCP client. */
export interface McpClientLike {
  callTool(params: { name: string; arguments?: Record<string, unknown> }): Promise<{
    content?: Array<{ type: string; text?: string; [k: string]: unknown }>;
    isError?: boolean;
    [k: string]: unknown;
  }>;
}

export interface DefineLocalMcpOptions {
  /**
   * Server label echoed back as `mcpServer` on every `local_tool_call`. The
   * SDK auto-prefixes each discovered tool's wire-level `name` with
   * `<this>_` so the model sees a non-colliding `<server>_<tool>` surface
   * — mirroring how MANTYX prefixes for `kind: "mcp"`.
   */
  name: string;
  /**
   * Streamable HTTP transport. Mutually exclusive with {@link command}.
   */
  url?: string;
  /**
   * Headers attached to every MCP request when using the HTTP transport.
   */
  headers?: Record<string, string>;
  /**
   * stdio transport: executable to launch. Mutually exclusive with
   * {@link url}.
   */
  command?: string;
  /** Arguments for the stdio child process. */
  args?: string[];
  /** Environment variables for the stdio child process. */
  env?: Record<string, string>;
  /** Working directory for the stdio child process. */
  cwd?: string;
}

export function defineLocalMcp(opts: DefineLocalMcpOptions): LocalMcpServer {
  assertToolName(opts.name);
  const hasHttp = typeof opts.url === "string" && opts.url.length > 0;
  const hasStdio = typeof opts.command === "string" && opts.command.length > 0;
  if (hasHttp && hasStdio) {
    throw new Error(
      "defineLocalMcp: pass either `url` (Streamable HTTP) or `command` (stdio), not both",
    );
  }
  if (!hasHttp && !hasStdio) {
    throw new Error(
      "defineLocalMcp: one of `url` (Streamable HTTP) or `command` (stdio) is required",
    );
  }
  if (hasHttp) {
    const url = opts.url as string;
    return {
      kind: "mcp_local",
      name: opts.name,
      http: {
        url,
        ...(opts.headers ? { headers: { ...opts.headers } } : {}),
      },
      stdio: undefined,
    };
  }
  const command = opts.command as string;
  return {
    kind: "mcp_local",
    name: opts.name,
    http: undefined,
    stdio: {
      command,
      ...(opts.args ? { args: [...opts.args] } : {}),
      ...(opts.env ? { env: { ...opts.env } } : {}),
      ...(opts.cwd ? { cwd: opts.cwd } : {}),
    },
  };
}

// ----------------------------------------------------------------- Union type

export type ToolRef =
  | MantyxToolRef
  | MantyxPluginToolRef
  | LocalTool
  | A2AToolRef
  | LocalA2ATool
  | McpToolRef
  | LocalMcpServer;

// --------------------------------------------------------- Type-guard helpers

export function isLocalTool(t: ToolRef): t is LocalTool {
  return t.kind === "local";
}
export function isLocalA2ATool(t: ToolRef): t is LocalA2ATool {
  return t.kind === "a2a_local";
}
export function isLocalMcpServer(t: ToolRef): t is LocalMcpServer {
  return t.kind === "mcp_local";
}

// ------------------------------------------------------------------- Internal

const TOOL_NAME_RE = /^[a-zA-Z0-9_]{1,64}$/;

function assertToolName(name: string): void {
  if (!TOOL_NAME_RE.test(name)) {
    throw new Error(
      `Invalid tool name ${JSON.stringify(name)}: must match /^[a-zA-Z0-9_]{1,64}$/`,
    );
  }
}

/**
 * Compose the wire-level (model-facing) tool name for a `mcp_local` entry.
 * The SDK auto-prefixes every discovered tool's bare name with the server
 * label so the model surface stays `<server>_<tool>`.
 */
export function prefixedMcpToolName(serverName: string, toolName: string): string {
  const prefix = `${serverName}_`;
  return toolName.startsWith(prefix) ? toolName : `${prefix}${toolName}`;
}
