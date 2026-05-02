import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MantyxClient } from "../src/index.js";
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

describe("AgentSession", () => {
  it("create + send round-trips a multi-turn conversation", async () => {
    const session = await client.createSession({ systemPrompt: "you are a chat bot" });
    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "first reply" }],
    };
    const out1 = await session.send("first");
    expect(out1.text).toBe("first reply");

    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "second reply" }],
    };
    const out2 = await session.send("second");
    expect(out2.text).toBe("second reply");

    const history = await session.history();
    expect(history.map((h) => h.content)).toEqual([
      "first",
      "first reply",
      "second",
      "second reply",
    ]);

    await session.end();
  });

  it("forwards createSession metadata and per-message metadata overrides", async () => {
    await client.createSession({
      systemPrompt: "you are a chat bot",
      metadata: { customer: "acme", env: "prod" },
    });
    expect(server.lastSessionCreateBody?.metadata).toEqual({
      customer: "acme",
      env: "prod",
    });

    const session = await client.createSession({ systemPrompt: "x" });
    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "ok" }],
    };
    await session.send("hello", { metadata: { trace_id: "trace_123" } });
    expect(server.lastSessionMessageBody?.metadata).toEqual({ trace_id: "trace_123" });
  });
});
