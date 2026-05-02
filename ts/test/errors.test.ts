import { describe, expect, it } from "vitest";
import {
  MantyxAuthError,
  MantyxError,
  MantyxNetworkError,
  MantyxRunError,
  MantyxToolError,
} from "../src/errors.js";

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

describe("MantyxRunError", () => {
  it("holds run id and subtype", () => {
    const e = new MantyxRunError("r1", "limit", "too long");
    expect(e.runId).toBe("r1");
    expect(e.subtype).toBe("limit");
    expect(e.message).toBe("too long");
  });
});
