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

  it("lists sessions and filters by metadata", async () => {
    await client.createSession({
      systemPrompt: "x",
      metadata: { customer: "acme", env: "prod" },
    });
    await client.createSession({
      systemPrompt: "x",
      metadata: { customer: "globex", env: "prod" },
    });

    const all = await client.listSessions();
    expect(all.total).toBe(2);

    const firstPage = await client.listSessions({ limit: 1 });
    expect(firstPage.sessions).toHaveLength(1);
    expect(firstPage.nextCursor).toBeTruthy();
    const secondPage = await client.listSessions({
      limit: 1,
      cursor: firstPage.nextCursor!,
    });
    expect(secondPage.sessions).toHaveLength(1);
    expect(secondPage.sessions[0]?.sessionId).not.toBe(
      firstPage.sessions[0]?.sessionId,
    );
    expect(secondPage.nextCursor).toBeNull();

    const filtered = await client.listSessions({
      metadata: { customer: "acme" },
    });
    expect(filtered.total).toBe(1);
    expect(filtered.sessions[0]?.metadata).toEqual({
      customer: "acme",
      env: "prod",
    });
    expect(filtered.sessions[0]?.status).toBe("active");
    expect(typeof filtered.sessions[0]?.creationDate).toBe("string");

    const none = await client.listSessions({
      metadata: { customer: "acme", env: "staging" },
    });
    expect(none.total).toBe(0);
  });

  it("replays a session as realtime-style event frames", async () => {
    const session = await client.createSession({ systemPrompt: "x" });
    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "two" }],
    };
    await session.send("one");
    server.scriptForNextSessionRun = {
      events: [{ type: "result", subtype: "success", text: "four" }],
    };
    await session.send("three");

    const full = await client.getSessionEvents(session.id);
    expect(full).toEqual([
      { seq: 1, type: "user_message", text: "one" },
      { seq: 2, type: "assistant_message", text: "two" },
      { seq: 3, type: "user_message", text: "three" },
      { seq: 4, type: "assistant_message", text: "four" },
    ]);

    const lastTwo = await client.getSessionEvents(session.id, {
      lastMessages: 2,
    });
    expect(lastTwo).toEqual([
      { seq: 3, type: "user_message", text: "three" },
      { seq: 4, type: "assistant_message", text: "four" },
    ]);

    const newestPage = await session.eventsPage({ lastMessages: 2 });
    expect(newestPage.events).toEqual(lastTwo);
    expect(newestPage.nextBeforeSeq).toBe(3);
    expect(newestPage.truncated).toBe(true);

    const olderPage = await session.eventsPage({
      beforeSeq: newestPage.nextBeforeSeq!,
    });
    expect(olderPage.events).toEqual([
      { seq: 1, type: "user_message", text: "one" },
      { seq: 2, type: "assistant_message", text: "two" },
    ]);
    expect(olderPage.nextBeforeSeq).toBeNull();
    expect(olderPage.truncated).toBe(false);
  });
});
