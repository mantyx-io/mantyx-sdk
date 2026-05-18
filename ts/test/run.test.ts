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

  it("surfaces terminal `error` triage attributes on MantyxRunError (truncation salvage)", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "assistant_message",
          text: '{"answer":"hello',
          turn: 0,
          finishReason: "max_tokens",
        },
        {
          type: "error",
          error: "Model output was truncated (stop_reason=max_tokens).",
          code: "truncation",
          errorClass: "truncation",
          finishReason: "max_tokens",
          partialText: '{"answer":"hello',
          retryable: false,
        },
      ],
    };
    const err = await client
      .runAgent({ systemPrompt: "x", prompt: "y" })
      .then(() => null)
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MantyxRunError);
    const e = err as MantyxRunError;
    expect(e.subtype).toBe("truncation");
    expect(e.code).toBe("truncation");
    expect(e.errorClass).toBe("truncation");
    expect(e.finishReason).toBe("max_tokens");
    expect(e.partialText).toBe('{"answer":"hello');
    expect(e.retryable).toBe(false);
    expect(e.message).toContain("truncated");
  });

  it("falls back to `code` when errorClass is absent (legacy server)", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "error",
          error: "boom",
          code: "worker_error",
        },
      ],
    };
    const err = await client
      .runAgent({ systemPrompt: "x", prompt: "y" })
      .then(() => null)
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MantyxRunError);
    const e = err as MantyxRunError;
    expect(e.subtype).toBe("worker_error");
    expect(e.errorClass).toBeUndefined();
    expect(e.finishReason).toBeUndefined();
    expect(e.partialText).toBeUndefined();
    expect(e.retryable).toBeUndefined();
  });

  it("surfaces enriched assistant_message fields (turn, finishReason, toolCalls) on the event stream", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "assistant_message",
          text: "calling search",
          turn: 0,
          finishReason: "tool_use",
          toolCalls: [{ id: "call_a", name: "search", input: { q: "hi" } }],
        },
        { type: "result", subtype: "success", text: "done" },
      ],
    };
    const collected: Array<Record<string, unknown>> = [];
    await client.runAgent({
      systemPrompt: "x",
      prompt: "y",
      onEvent: (ev) => collected.push(ev as unknown as Record<string, unknown>),
    });
    const msg = collected.find((ev) => ev.type === "assistant_message");
    expect(msg).toMatchObject({
      type: "assistant_message",
      text: "calling search",
      turn: 0,
      finishReason: "tool_use",
      toolCalls: [{ id: "call_a", name: "search", input: { q: "hi" } }],
    });
  });

  it("surfaces cost-attribution fields (tokens/turns/model) from the terminal result event", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "result",
          subtype: "success",
          text: "Hello world",
          tokens: {
            inputTokens: 1283,
            cachedTokens: 512,
            reasoningTokens: 96,
            outputTokens: 240,
          },
          turns: 3,
          model: {
            id: "platform:demo",
            provider: "openai",
            vendorModelId: "gpt-test",
            reasoningEffort: "low",
          },
        },
      ],
    };
    const result = await client.runAgent({ systemPrompt: "x", prompt: "y" });
    expect(result.tokens).toEqual({
      inputTokens: 1283,
      cachedTokens: 512,
      reasoningTokens: 96,
      outputTokens: 240,
    });
    expect(result.turns).toBe(3);
    expect(result.model).toEqual({
      id: "platform:demo",
      provider: "openai",
      vendorModelId: "gpt-test",
      reasoningEffort: "low",
    });
  });

  it("leaves tokens/turns/model undefined against legacy servers (no usage data)", async () => {
    server.scriptForNextRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    const result = await client.runAgent({ systemPrompt: "x", prompt: "y" });
    expect(result.tokens).toBeUndefined();
    expect(result.turns).toBeUndefined();
    expect(result.model).toBeUndefined();
  });

  it("surfaces cost-attribution fields on terminal error events too", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "error",
          error: "Model output was truncated (stop_reason=max_tokens).",
          errorClass: "truncation",
          finishReason: "max_tokens",
          partialText: '{"answer":"hello',
          retryable: false,
          tokens: {
            inputTokens: 8190,
            cachedTokens: 0,
            reasoningTokens: 0,
            outputTokens: 1024,
          },
          turns: 1,
          model: {
            id: "provider:cmf",
            provider: "google",
            vendorModelId: "gemini-2.5-pro",
          },
        },
      ],
    };
    const err = await client
      .runAgent({ systemPrompt: "x", prompt: "y" })
      .then(() => null)
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MantyxRunError);
    const e = err as MantyxRunError;
    expect(e.tokens).toEqual({
      inputTokens: 8190,
      cachedTokens: 0,
      reasoningTokens: 0,
      outputTokens: 1024,
    });
    expect(e.turns).toBe(1);
    expect(e.model).toEqual({
      id: "provider:cmf",
      provider: "google",
      vendorModelId: "gemini-2.5-pro",
    });
    // The "no usage data" sentinel is an empty / undefined provider —
    // here the wire surfaces "google" so model.provider is populated.
    expect(e.model?.provider).toBe("google");
  });

  it("clamps malformed token buckets to non-negative integers", async () => {
    server.scriptForNextRun = {
      events: [
        {
          type: "result",
          subtype: "success",
          text: "ok",
          tokens: {
            inputTokens: -10,
            cachedTokens: Number.NaN,
            // missing reasoningTokens entirely
            outputTokens: 12.7,
          },
          turns: -1,
          model: { id: "x", provider: "openai", vendorModelId: "y" },
        },
      ],
    };
    const result = await client.runAgent({ systemPrompt: "x", prompt: "y" });
    expect(result.tokens).toEqual({
      inputTokens: 0,
      cachedTokens: 0,
      reasoningTokens: 0,
      outputTokens: 12,
    });
    expect(result.turns).toBe(0);
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
