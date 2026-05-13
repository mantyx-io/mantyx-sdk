import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MantyxClient } from "../src/index.js";
import {
  MantyxAuthError,
  MantyxError,
  MantyxNetworkError,
  MantyxRunError,
  MantyxScopeError,
  MantyxToolError,
} from "../src/errors.js";
import { MockServer } from "./helpers/mock-server.js";

describe("MantyxError", () => {
  it("exposes default code and optional status/hint", () => {
    const e = new MantyxError("m", { code: "c", status: 400, hint: "h" });
    expect(e).toBeInstanceOf(Error);
    expect(e.name).toBe("MantyxError");
    expect(e.message).toBe("m");
    expect(e.code).toBe("c");
    expect(e.status).toBe(400);
    expect(e.hint).toBe("h");
  });
});

describe("MantyxAuthError", () => {
  it("is a 401 unauthorized class", () => {
    const e = new MantyxAuthError();
    expect(e.code).toBe("unauthorized");
    expect(e.status).toBe(401);
  });

  it("default message mentions both credential kinds (API key + OAuth)", () => {
    const e = new MantyxAuthError();
    expect(e.message).toContain("API key");
    expect(e.message.toLowerCase()).toContain("oauth");
  });
});

describe("MantyxScopeError", () => {
  it("is a 403 insufficient_scope class that carries the required scopes", () => {
    const e = new MantyxScopeError("missing scope: runs:write", ["runs:write"]);
    expect(e.name).toBe("MantyxScopeError");
    expect(e.code).toBe("insufficient_scope");
    expect(e.status).toBe(403);
    expect([...e.requiredScopes]).toEqual(["runs:write"]);
  });

  it("supports multi-scope routes", () => {
    const e = new MantyxScopeError("missing scopes", ["runs:read", "runs:write"]);
    expect([...e.requiredScopes]).toEqual(["runs:read", "runs:write"]);
  });
});

describe("MantyxNetworkError", () => {
  it("attaches a cause for errors.is-style inspection", () => {
    const cause = new TypeError("fail");
    const e = new MantyxNetworkError("n", { cause });
    expect(e.name).toBe("MantyxNetworkError");
    expect(e.code).toBe("network");
    const err = e as Error & { cause?: unknown };
    expect(err.cause).toBe(cause);
  });
});

describe("MantyxToolError", () => {
  it("records toolName", () => {
    const e = new MantyxToolError("t", "oops");
    expect(e.toolName).toBe("t");
    expect(e.message).toContain("t");
    expect(e.message).toContain("oops");
  });
});

describe("MantyxClient credential resolution", () => {
  let server: MockServer;

  beforeEach(async () => {
    server = new MockServer();
    await server.start();
  });

  afterEach(async () => {
    await server.stop();
  });

  it("accepts apiKey", () => {
    const client = new MantyxClient({
      apiKey: "mantyx_test",
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    expect(client.options.apiKey).toBe("mantyx_test");
  });

  it("accepts accessToken (OAuth) as an alias for apiKey", () => {
    const client = new MantyxClient({
      accessToken: "mantyx_at_test",
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    expect(client.options.apiKey).toBe("mantyx_at_test");
  });

  it("ships the same `Authorization: Bearer` header for either kind", async () => {
    const client = new MantyxClient({
      accessToken: "mantyx_at_oauth",
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    await client.listModels();
    expect(server.lastAuthHeader).toBe("Bearer mantyx_at_oauth");
  });

  it("rejects passing both credentials at construction time", () => {
    expect(
      () =>
        new MantyxClient({
          apiKey: "mantyx_x",
          accessToken: "mantyx_at_y",
          workspaceSlug: "demo",
          baseUrl: server.baseUrl(),
        }),
    ).toThrow(MantyxError);
  });

  it("rejects when neither credential is set", () => {
    expect(
      () =>
        new MantyxClient({
          workspaceSlug: "demo",
          baseUrl: server.baseUrl(),
        } as unknown as ConstructorParameters<typeof MantyxClient>[0]),
    ).toThrow(MantyxError);
  });

  it("surfaces 403 insufficient_scope as MantyxScopeError with the required scopes", async () => {
    const client = new MantyxClient({
      accessToken: "mantyx_at_test",
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.failScope = { required: ["runs:write"] };
    await expect(client.listModels()).rejects.toMatchObject({
      name: "MantyxScopeError",
      code: "insufficient_scope",
      status: 403,
      requiredScopes: ["runs:write"],
    });
  });

  it("parses array-shaped `required` values for multi-scope routes", async () => {
    const client = new MantyxClient({
      accessToken: "mantyx_at_test",
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.failScope = { required: ["runs:read", "runs:write"] };
    await expect(client.listModels()).rejects.toMatchObject({
      name: "MantyxScopeError",
      requiredScopes: ["runs:read", "runs:write"],
    });
  });
});

describe("MantyxRunError", () => {
  it("holds run id and subtype", () => {
    const e = new MantyxRunError("r1", "limit", "too long");
    expect(e.runId).toBe("r1");
    expect(e.subtype).toBe("limit");
    expect(e.message).toBe("too long");
    expect(e.errorClass).toBeUndefined();
    expect(e.finishReason).toBeUndefined();
    expect(e.partialText).toBeUndefined();
    expect(e.retryable).toBeUndefined();
  });

  it("carries optional triage attributes (errorClass, finishReason, partialText, retryable)", () => {
    const e = new MantyxRunError("r1", "truncation", "truncated", {
      errorClass: "truncation",
      finishReason: "max_tokens",
      partialText: '{"answer":"hello',
      retryable: false,
    });
    expect(e.errorClass).toBe("truncation");
    expect(e.finishReason).toBe("max_tokens");
    expect(e.partialText).toBe('{"answer":"hello');
    expect(e.retryable).toBe(false);
  });
});
