import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { z } from "zod";
import {
  defineLocalA2A,
  defineLocalMcp,
  defineLocalTool,
  isLocalA2ATool,
  isLocalMcpServer,
  MantyxClient,
  MantyxError,
  mantyxA2A,
  mantyxMcp,
} from "../src/index.js";
import type { LocalMcpServer, ResolvedMcpServer } from "../src/tools.js";
import { MockServer } from "./helpers/mock-server.js";

let server: MockServer;
let client: MantyxClient;

beforeEach(async () => {
  server = new MockServer();
  await server.start();
  client = new MantyxClient({
    apiKey: "test-key",
    workspaceSlug: "demo",
    baseUrl: server.baseUrl(),
  });
});

afterEach(async () => {
  await server.stop();
});

/**
 * Seed an `mcp_local` tool ref with a fake resolved snapshot, bypassing the
 * actual `@modelcontextprotocol/sdk` transport. The fake `client.callTool`
 * captures invocations so tests can assert what the SDK forwarded.
 */
function seedMcpResolution(
  ref: LocalMcpServer,
  opts: {
    serverInfo?: { name: string; version?: string; [k: string]: unknown };
    tools: Array<{
      name: string;
      description?: string;
      inputSchema: Record<string, unknown>;
      annotations?: Record<string, unknown>;
    }>;
    onCall?: (params: { name: string; arguments?: Record<string, unknown> }) => string;
  },
): { calls: Array<{ name: string; arguments?: Record<string, unknown> }>; close: () => void } {
  const calls: Array<{ name: string; arguments?: Record<string, unknown> }> = [];
  let closed = false;
  const resolved: ResolvedMcpServer = {
    serverInfo: opts.serverInfo ?? { name: ref.name, version: "test" },
    tools: opts.tools,
    client: {
      async callTool(params) {
        calls.push(params);
        const text = opts.onCall ? opts.onCall(params) : `ok:${params.name}`;
        return { content: [{ type: "text", text }] };
      },
    },
    close: async () => {
      closed = true;
    },
  };
  (ref as { _resolved?: ResolvedMcpServer })._resolved = resolved;
  return {
    calls,
    close: () => {
      if (closed) return;
      closed = true;
    },
  };
}

describe("tool ref serialization", () => {
  it("serializes mantyxA2A and mantyxMcp tool refs verbatim onto the wire", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({
      systemPrompt: "x",
      prompt: "y",
      tools: [
        mantyxA2A({
          name: "billing",
          description: "Talk to the Acme billing agent.",
          agentCardUrl: "https://billing.acme.com/.well-known/agent-card.json",
          headers: { Authorization: "Bearer abc" },
          contextId: "ctx_1",
        }),
        mantyxMcp({
          name: "github",
          url: "https://mcp.github.com/v1",
          headers: { Authorization: "Bearer gh_pat" },
          toolFilter: ["search_repos", "read_file"],
        }),
      ],
    });
    const tools = (server.lastRunCreateBody?.tools ?? []) as Array<Record<string, unknown>>;
    expect(tools).toHaveLength(2);
    expect(tools[0]).toEqual({
      kind: "a2a",
      name: "billing",
      description: "Talk to the Acme billing agent.",
      agentCardUrl: "https://billing.acme.com/.well-known/agent-card.json",
      headers: { Authorization: "Bearer abc" },
      contextId: "ctx_1",
    });
    expect(tools[1]).toEqual({
      kind: "mcp",
      name: "github",
      url: "https://mcp.github.com/v1",
      headers: { Authorization: "Bearer gh_pat" },
      toolFilter: ["search_repos", "read_file"],
    });
  });

  it("auto-resolves defineLocalA2A and ships the fetched Agent Card on the wire", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    server.a2aAgentCardResponse = {
      name: "Acme HR",
      description: "Answers HR policy questions.",
      url: `${server.baseUrl()}/a2a/rpc`,
      protocolVersion: "0.3.0",
      skills: [{ id: "pto_lookup", name: "PTO lookup" }],
    };
    const a2a = defineLocalA2A({
      name: "intranet_hr",
      agentCardUrl: `${server.baseUrl()}/a2a/agent-card.json`,
      headers: { Authorization: "Bearer intra-token" },
    });
    expect(isLocalA2ATool(a2a)).toBe(true);

    await client.runAgent({ systemPrompt: "x", prompt: "y", tools: [a2a] });
    const tools = (server.lastRunCreateBody?.tools ?? []) as Array<Record<string, unknown>>;
    expect(tools).toHaveLength(1);
    // Per `docs/wire-protocol.md` §3.1 — `kind: "a2a_local"` ships the
    // resolved Agent Card; the user only supplied a URL.
    expect(tools[0]).toEqual({
      kind: "a2a_local",
      name: "intranet_hr",
      agentCard: {
        name: "Acme HR",
        description: "Answers HR policy questions.",
        url: `${server.baseUrl()}/a2a/rpc`,
        protocolVersion: "0.3.0",
        skills: [{ id: "pto_lookup", name: "PTO lookup" }],
      },
    });
    expect(tools[0]).not.toHaveProperty("agentCardUrl");
  });

  it("requires `agentCardUrl` on defineLocalA2A", () => {
    expect(() =>
      defineLocalA2A({
        name: "intranet_hr",
        // @ts-expect-error — exercise runtime guard
        agentCardUrl: undefined,
      }),
    ).toThrow();
  });

  it("ships the resolved MCP catalog on the wire (auto-prefixed names + serverInfo + annotations)", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    const mcp = defineLocalMcp({ name: "fs", url: "http://localhost:9999/mcp" });
    expect(isLocalMcpServer(mcp)).toBe(true);
    seedMcpResolution(mcp, {
      serverInfo: { name: "mcp-server-filesystem", version: "0.4.1" },
      tools: [
        {
          name: "read_file",
          description: "Read a file from disk.",
          inputSchema: { type: "object", properties: { path: { type: "string" } } },
          annotations: { readOnlyHint: true, openWorldHint: false },
        },
      ],
    });

    await client.runAgent({ systemPrompt: "x", prompt: "y", tools: [mcp] });
    const tools = (server.lastRunCreateBody?.tools ?? []) as Array<Record<string, unknown>>;
    expect(tools).toHaveLength(1);
    expect(tools[0]).toEqual({
      kind: "mcp_local",
      name: "fs",
      serverInfo: { name: "mcp-server-filesystem", version: "0.4.1" },
      tools: [
        {
          // SDK auto-prefixes upstream `read_file` → wire `fs_read_file`,
          // mirroring how MANTYX prefixes for `kind: "mcp"`.
          name: "fs_read_file",
          description: "Read a file from disk.",
          inputSchema: { type: "object", properties: { path: { type: "string" } } },
          annotations: { readOnlyHint: true, openWorldHint: false },
        },
      ],
    });
  });

  it("rejects defineLocalMcp called with both `url` and `command` (and with neither)", () => {
    expect(() =>
      defineLocalMcp({ name: "fs", url: "http://x/mcp", command: "mcp-server-fs" }),
    ).toThrow();
    expect(() => defineLocalMcp({ name: "fs" })).toThrow();
  });

  it("rejects bad tool names and missing url/agentCardUrl on the public surface", () => {
    expect(() => mantyxA2A({ name: "bad name", agentCardUrl: "https://x" })).toThrow();
    expect(() => mantyxMcp({ name: "x", url: "" })).toThrow();
    expect(() => defineLocalA2A({ name: "x", agentCardUrl: "" })).toThrow();
  });
});

describe("reasoningLevel forwarding", () => {
  it("forwards string anchors verbatim", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", reasoningLevel: "medium" });
    expect(server.lastRunCreateBody?.reasoningLevel).toBe("medium");
  });

  it("forwards numeric levels (truncated to integer)", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", reasoningLevel: 80 });
    expect(server.lastRunCreateBody?.reasoningLevel).toBe(80);
  });

  it("rejects out-of-range numeric levels and unknown string anchors locally", async () => {
    await expect(
      client.runAgent({ systemPrompt: "x", prompt: "y", reasoningLevel: 200 }),
    ).rejects.toBeInstanceOf(MantyxError);
    await expect(
      client.runAgent({
        systemPrompt: "x",
        prompt: "y",
        reasoningLevel: "ultra" as unknown as "high",
      }),
    ).rejects.toBeInstanceOf(MantyxError);
  });

  it("omits the field entirely when not set", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y" });
    expect(server.lastRunCreateBody?.reasoningLevel).toBeUndefined();
  });

  it("forwards reasoningLevel on session create and per-message override", async () => {
    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    const session = await client.createSession({
      systemPrompt: "x",
      reasoningLevel: "low",
    });
    expect(server.lastSessionCreateBody?.reasoningLevel).toBe("low");
    await session.send("hi", { reasoningLevel: 90 });
    expect(server.lastSessionMessageBody?.reasoningLevel).toBe(90);
  });
});

describe("local_tool_call dispatch", () => {
  it("dispatches `kind: \"a2a_local\"` events by POSTing message/send to the resolved Agent Card URL", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "tu_a2a",
          name: "intranet_hr",
          kind: "a2a_local",
          args: { message: "When does PTO reset?" },
          // The wire echoes the full Agent Card; the SDK's dispatcher
          // ignores it (it has the cached card from the original resolve).
          agentCard: { name: "Acme HR" },
          awaitToolResult: true,
        },
        { type: "result", subtype: "success", text: "done" },
      ],
    };
    server.a2aReplyText = "PTO resets on Jan 1.";
    const a2a = defineLocalA2A({
      name: "intranet_hr",
      agentCardUrl: `${server.baseUrl()}/a2a/agent-card.json`,
      headers: { Authorization: "Bearer intra-token" },
    });

    const out = await client.runAgent({
      systemPrompt: "x",
      prompt: "ask hr",
      tools: [a2a],
    });

    expect(out.text).toBe("done");
    // The SDK posted JSON-RPC `message/send` to the card's URL with the
    // model's `message` argument verbatim, and forwarded the auth header.
    expect(server.lastA2ARequest?.method).toBe("message/send");
    expect(server.lastA2ARequest?.message).toBe("When does PTO reset?");
    expect(server.lastA2ARequest?.headers.authorization).toBe("Bearer intra-token");
    expect(server.lastToolResult?.payload).toEqual({
      toolUseId: "tu_a2a",
      result: "PTO resets on Jan 1.",
    });
  });

  it("dispatches `kind: \"mcp_local\"` events via the cached MCP client's tools/call", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "tu_mcp",
          name: "fs_read_file",
          kind: "mcp_local",
          mcpServer: "fs",
          mcpToolName: "fs_read_file",
          mcpServerInfo: { name: "mcp-server-filesystem", version: "0.4.1" },
          args: { path: "/etc/hosts" },
          awaitToolResult: true,
        },
        { type: "result", subtype: "success", text: "done" },
      ],
    };
    const mcp = defineLocalMcp({ name: "fs", url: "http://localhost:9999/mcp" });
    const { calls } = seedMcpResolution(mcp, {
      tools: [
        { name: "read_file", inputSchema: { type: "object" } },
      ],
      onCall: () => "127.0.0.1 localhost\n",
    });

    const out = await client.runAgent({
      systemPrompt: "x",
      prompt: "read hosts",
      tools: [mcp],
    });

    expect(out.text).toBe("done");
    // The SDK strips the `<server>_` prefix before forwarding to MCP
    // `tools/call` (the upstream server uses the bare name).
    expect(calls).toEqual([{ name: "read_file", arguments: { path: "/etc/hosts" } }]);
    expect(server.lastToolResult?.payload).toEqual({
      toolUseId: "tu_mcp",
      result: "127.0.0.1 localhost\n",
    });
  });

  it("posts an error tool-result when no handler is registered for the discriminator", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "tu_unknown",
          name: "nope",
          kind: "a2a_local",
          args: { message: "hi" },
          awaitToolResult: true,
        },
        { type: "result", subtype: "success", text: "done" },
      ],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", tools: [] });
    expect(server.lastToolResult?.payload).toMatchObject({
      toolUseId: "tu_unknown",
      error: expect.stringContaining("No local A2A handler"),
    });
  });

  it("falls back to the generic `kind: \"local\"` registry when the event omits `kind`", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "tu_legacy",
          name: "echo",
          args: { value: "hi" },
          awaitToolResult: true,
        },
        { type: "result", subtype: "success", text: "done" },
      ],
    };
    const tool = defineLocalTool({
      name: "echo",
      parameters: z.object({ value: z.string() }),
      execute: ({ value }) => `result:${value}`,
    });
    await client.runAgent({ systemPrompt: "x", prompt: "y", tools: [tool] });
    expect(server.lastToolResult?.payload).toEqual({
      toolUseId: "tu_legacy",
      result: "result:hi",
    });
  });
});
