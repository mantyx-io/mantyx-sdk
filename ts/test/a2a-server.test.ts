import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MantyxClient, MantyxError } from "../src/index.js";
import {
  MantyxAgentExecutor,
  serveAgentOverA2A,
} from "../src/a2a-server.js";
import type { ServeAgentOverA2AHandle } from "../src/a2a-server.js";
import type {
  Message,
  Task,
  TaskStatusUpdateEvent,
} from "@a2a-js/sdk";
import type {
  AgentExecutionEvent,
  ExecutionEventBus,
  RequestContext,
} from "@a2a-js/sdk/server";
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
 * Tiny in-memory event bus that captures everything the executor publishes
 * so the tests can assert on the wire shape without standing up the full
 * `@a2a-js/sdk/server` request handler.
 */
class CapturingEventBus implements ExecutionEventBus {
  events: AgentExecutionEvent[] = [];
  finishedCalled = false;

  publish(event: AgentExecutionEvent): void {
    this.events.push(event);
  }
  finished(): void {
    this.finishedCalled = true;
  }
  // Unused EventEmitter surface — return self for chaining.
  on(): this {
    return this;
  }
  off(): this {
    return this;
  }
  once(): this {
    return this;
  }
  removeAllListeners(): this {
    return this;
  }
}

function userMessage(text: string, taskId: string, contextId: string): Message {
  return {
    kind: "message",
    messageId: `msg_${Math.random().toString(36).slice(2)}`,
    role: "user",
    parts: [{ kind: "text", text }],
    taskId,
    contextId,
  };
}

function ctx(taskId: string, contextId: string, text: string, task?: Task): RequestContext {
  return {
    userMessage: userMessage(text, taskId, contextId),
    taskId,
    contextId,
    ...(task ? { task } : {}),
    referenceTasks: [],
  } as unknown as RequestContext;
}

describe("MantyxAgentExecutor — input validation", () => {
  it("requires either agentId or systemPrompt", () => {
    expect(() => new MantyxAgentExecutor({ client, agent: {} as never })).toThrow(MantyxError);
  });
  it("accepts agentId", () => {
    expect(
      () => new MantyxAgentExecutor({ client, agent: { agentId: "agent_abc" } }),
    ).not.toThrow();
  });
  it("accepts ephemeral systemPrompt", () => {
    expect(
      () =>
        new MantyxAgentExecutor({
          client,
          agent: { systemPrompt: "you are helpful" },
        }),
    ).not.toThrow();
  });
});

describe("MantyxAgentExecutor — stateless execute()", () => {
  it("publishes Task → working → completed and finishes the bus", async () => {
    server.scriptForNextRun = {
      events: [
        { type: "assistant_delta", text: "Hi" },
        { type: "assistant_delta", text: " there" },
      ],
      finalText: "Hi there",
    };

    const exec = new MantyxAgentExecutor({
      client,
      agent: { systemPrompt: "you are helpful" },
      conversation: "stateless",
    });
    const bus = new CapturingEventBus();
    await exec.execute(ctx("task_1", "ctx_1", "Greet me"), bus);

    const initialTask = bus.events[0] as Task;
    expect(initialTask.kind).toBe("task");
    expect(initialTask.id).toBe("task_1");
    expect(initialTask.contextId).toBe("ctx_1");
    expect(initialTask.status.state).toBe("submitted");

    const working = bus.events[1] as TaskStatusUpdateEvent;
    expect(working.kind).toBe("status-update");
    expect(working.status.state).toBe("working");
    expect(working.final).toBe(false);

    // Two delta status-updates, then a final completed status-update.
    const deltas = bus.events.filter(
      (e) => e.kind === "status-update" && e.status.state === "working" && e.status.message,
    ) as TaskStatusUpdateEvent[];
    expect(deltas).toHaveLength(2);
    expect((deltas[0]!.status.message!.parts[0] as { text: string }).text).toBe("Hi");
    expect((deltas[1]!.status.message!.parts[0] as { text: string }).text).toBe(" there");

    const final = bus.events[bus.events.length - 1] as TaskStatusUpdateEvent;
    expect(final.kind).toBe("status-update");
    expect(final.status.state).toBe("completed");
    expect(final.final).toBe(true);
    expect((final.status.message!.parts[0] as { text: string }).text).toBe("Hi there");
    expect(bus.finishedCalled).toBe(true);
  });

  it("forwards a stateless run to runAgent (no session created)", async () => {
    server.scriptForNextRun = { events: [], finalText: "hello" };

    const exec = new MantyxAgentExecutor({
      client,
      agent: { systemPrompt: "you are helpful" },
      conversation: "stateless",
    });
    await exec.execute(ctx("task_a", "ctx_a", "hi"), new CapturingEventBus());

    expect(server.lastRunCreateBody?.systemPrompt).toBe("you are helpful");
    expect(server.lastRunCreateBody?.prompt).toBe("hi");
    expect(server.lastSessionCreateBody).toBeNull();
  });
});

describe("MantyxAgentExecutor — auto session mapping", () => {
  it("creates one session per contextId and reuses it on subsequent calls", async () => {
    server.scriptForNextSessionRun = { events: [], finalText: "first" };

    const exec = new MantyxAgentExecutor({
      client,
      agent: { agentId: "agent_xyz" },
    });

    await exec.execute(ctx("task_1", "ctx_one", "hello"), new CapturingEventBus());
    expect(server.lastSessionCreateBody?.agentId).toBe("agent_xyz");
    const meta = server.lastSessionCreateBody?.metadata as
      | Record<string, string>
      | undefined;
    expect(meta?.a2a_context_id).toBe("ctx_one");

    server.scriptForNextSessionRun = { events: [], finalText: "second" };
    server.lastSessionCreateBody = null;

    await exec.execute(ctx("task_2", "ctx_one", "follow-up"), new CapturingEventBus());
    // Same contextId → no new session was created.
    expect(server.lastSessionCreateBody).toBeNull();
    expect(server.lastSessionMessageBody?.prompt).toBe("follow-up");

    await exec.close();
  });

  it("opens a fresh session for a different contextId", async () => {
    server.scriptForNextSessionRun = { events: [], finalText: "alpha" };
    const exec = new MantyxAgentExecutor({
      client,
      agent: { systemPrompt: "you are helpful" },
    });

    await exec.execute(ctx("task_1", "ctx_alpha", "hi"), new CapturingEventBus());

    server.scriptForNextSessionRun = { events: [], finalText: "beta" };
    server.lastSessionCreateBody = null;

    await exec.execute(ctx("task_2", "ctx_beta", "hi"), new CapturingEventBus());
    const beta = server.lastSessionCreateBody as Record<string, unknown> | null;
    expect(beta).not.toBeNull();
    const meta = beta?.metadata as Record<string, string> | undefined;
    expect(meta?.a2a_context_id).toBe("ctx_beta");

    await exec.close();
  });
});

describe("MantyxAgentExecutor — error mapping", () => {
  it("publishes a 'failed' status-update when the run errors", async () => {
    // No scriptForNextRun → the mock returns a 500 on POST /agent-runs.
    const exec = new MantyxAgentExecutor({
      client,
      agent: { systemPrompt: "you are helpful" },
      conversation: "stateless",
    });
    const bus = new CapturingEventBus();
    await exec.execute(ctx("task_err", "ctx_err", "hi"), bus);

    const final = bus.events[bus.events.length - 1] as TaskStatusUpdateEvent;
    expect(final.kind).toBe("status-update");
    expect(final.status.state).toBe("failed");
    expect(final.final).toBe(true);
    expect(bus.finishedCalled).toBe(true);
  });
});

describe("serveAgentOverA2A", () => {
  let handle: ServeAgentOverA2AHandle | null = null;

  afterEach(async () => {
    if (handle) {
      await handle.close();
      handle = null;
    }
  });

  it("publishes the Agent Card at /.well-known/agent-card.json", async () => {
    handle = await serveAgentOverA2A({
      client,
      agent: { systemPrompt: "you are helpful" },
      agentCard: {
        name: "MANTYX Test",
        description: "test",
        protocolVersion: "0.3.0",
        version: "1.0.0",
        url: "http://localhost",
        skills: [
          { id: "chat", name: "Chat", description: "Say hello", tags: ["chat"] },
        ],
        capabilities: { streaming: true, pushNotifications: false },
        defaultInputModes: ["text"],
        defaultOutputModes: ["text"],
      },
      port: 0,
      host: "127.0.0.1",
    });

    const res = await fetch(`${handle.url}/.well-known/agent-card.json`);
    expect(res.status).toBe(200);
    const card = (await res.json()) as { name: string };
    expect(card.name).toBe("MANTYX Test");
  });

  it("returns a final Message via JSON-RPC message/send", async () => {
    server.scriptForNextSessionRun = { events: [], finalText: "Hello, world!" };
    handle = await serveAgentOverA2A({
      client,
      agent: { systemPrompt: "you are helpful" },
      agentCard: {
        name: "MANTYX Test",
        description: "test",
        protocolVersion: "0.3.0",
        version: "1.0.0",
        url: "http://localhost",
        skills: [
          { id: "chat", name: "Chat", description: "Say hello", tags: ["chat"] },
        ],
        capabilities: { streaming: true, pushNotifications: false },
        defaultInputModes: ["text"],
        defaultOutputModes: ["text"],
      },
      port: 0,
      host: "127.0.0.1",
    });

    const rpcBody = {
      jsonrpc: "2.0",
      id: 1,
      method: "message/send",
      params: {
        message: {
          kind: "message",
          messageId: "u1",
          role: "user",
          parts: [{ kind: "text", text: "hi there" }],
        },
      },
    };
    const res = await fetch(handle.url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(rpcBody),
    });
    expect(res.status).toBe(200);
    const json = (await res.json()) as { result: Task | Message };
    // For non-streaming send, the official handler returns the final task or message.
    // Accept either shape so this test isn't brittle to handler internals — assert
    // we got back something with the expected text payload.
    const stringified = JSON.stringify(json);
    expect(stringified).toContain("Hello, world!");
  });
});
