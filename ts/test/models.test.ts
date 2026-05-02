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

describe("MantyxClient.listModels", () => {
  it("returns the catalog from the server", async () => {
    const out = await client.listModels();
    expect(out.models).toHaveLength(1);
    expect(out.models[0]?.id).toBe("platform:demo");
    expect(out.defaultModelId).toBe("platform:demo");
  });
});
