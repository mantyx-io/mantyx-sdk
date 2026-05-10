import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { z } from "zod";
import { MantyxAuthError, MantyxClient, MantyxError, MantyxRunError, defineLocalTool } from "../src/index.js";
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

describe("MantyxClient.runAgent", () => {
  it("runs a one-shot agent and returns the final text", async () => {
    server.scriptForNextRun = {
      events: [
        { type: "assistant_delta", text: "Hello " },
        { type: "assistant_delta", text: "world" },
        { type: "result", subtype: "success", text: "Hello world" },
      ],
    };
    const deltas: string[] = [];
    const result = await client.runAgent({
      systemPrompt: "be helpful",
      prompt: "say hi",
      onAssistantDelta: (s) => deltas.push(s),
    });
    expect(result.text).toBe("Hello world");
    expect(deltas.join("")).toBe("Hello world");
    expect(server.lastAuthHeader).toBe("Bearer test-key");
  });

  it("dispatches local tools and posts results back", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "local_tool_call",
          toolUseId: "t1",
          name: "echo",
          args: { value: "abc" },
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
    const out = await client.runAgent({
      systemPrompt: "x",
      prompt: "go",
      tools: [tool],
    });
    expect(out.text).toBe("done");
    expect(server.lastToolResult?.payload).toEqual({
      toolUseId: "t1",
      result: "result:abc",
    });
  });

  it("surfaces server-reported errors as MantyxRunError", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "error_other", text: "bad" } as { type: "result" } & { subtype?: string; text?: string }],
    };
    await expect(
      client.runAgent({ systemPrompt: "x", prompt: "y" }),
    ).rejects.toBeInstanceOf(MantyxRunError);
  });

  it("rejects when the server returns 401", async () => {
    server.failAuth = true;
    await expect(
      client.runAgent({ systemPrompt: "x", prompt: "y" }),
    ).rejects.toBeInstanceOf(MantyxAuthError);
  });

  it("sends agentId and merges extra local tools when targeting a persisted agent", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    const tool = defineLocalTool({
      name: "echo",
      parameters: z.object({ value: z.string() }),
      execute: ({ value }) => value,
    });
    await client.runAgent({
      agentId: "agent_abc",
      prompt: "hi",
      tools: [tool],
    });
    expect(server.lastRunCreateBody).toMatchObject({
      agentId: "agent_abc",
      prompt: "hi",
    });
    expect(server.lastRunCreateBody?.systemPrompt).toBeUndefined();
    expect(Array.isArray(server.lastRunCreateBody?.tools)).toBe(true);
  });

  it("rejects locally when neither agentId nor systemPrompt is set", async () => {
    // Both agentId and systemPrompt are typed as optional so the spec is type-valid;
    // the rejection comes from the runtime guard inside `runAgent`.
    await expect(
      client.runAgent({ prompt: "no spec" }),
    ).rejects.toBeInstanceOf(MantyxError);
  });

  it("forwards `metadata` on the create-run body so the dashboard can filter by it", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({
      systemPrompt: "x",
      prompt: "y",
      metadata: { customer: "acme", env: "prod" },
    });
    expect(server.lastRunCreateBody?.metadata).toEqual({
      customer: "acme",
      env: "prod",
    });
  });

  it("omits empty metadata objects from the create-run body", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", metadata: {} });
    expect(server.lastRunCreateBody?.metadata).toBeUndefined();
  });

  it("forwards loopDetection thresholds and toolBudgets verbatim", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({
      systemPrompt: "x",
      prompt: "y",
      loopDetection: { consecutiveThreshold: 4, hardCutoffThreshold: 8 },
      toolBudgets: {
        recall: { maxCalls: 3 },
        scary_tool: { maxCalls: 0 },
      },
    });
    expect(server.lastRunCreateBody?.loopDetection).toEqual({
      consecutiveThreshold: 4,
      hardCutoffThreshold: 8,
    });
    expect(server.lastRunCreateBody?.toolBudgets).toEqual({
      recall: { maxCalls: 3 },
      scary_tool: { maxCalls: 0 },
    });
  });

  it("forwards `loopDetection: false` to disable the guard", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", loopDetection: false });
    expect(server.lastRunCreateBody?.loopDetection).toBe(false);
  });

  it("rejects loopDetection with hardCutoffThreshold <= consecutiveThreshold locally", async () => {
    await expect(
      client.runAgent({
        systemPrompt: "x",
        prompt: "y",
        loopDetection: { consecutiveThreshold: 5, hardCutoffThreshold: 5 },
      }),
    ).rejects.toBeInstanceOf(MantyxError);
  });

  it("rejects toolBudgets with negative maxCalls locally", async () => {
    await expect(
      client.runAgent({
        systemPrompt: "x",
        prompt: "y",
        toolBudgets: { recall: { maxCalls: -1 } },
      }),
    ).rejects.toBeInstanceOf(MantyxError);
  });

  it("forwards an empty toolBudgets object so the server clears its defaults", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await client.runAgent({ systemPrompt: "x", prompt: "y", toolBudgets: {} });
    expect(server.lastRunCreateBody?.toolBudgets).toEqual({});
  });
});
