import { describe, expect, it } from "vitest";
import { MantyxClient, MantyxError } from "../src/index.js";
import { MockServer } from "./helpers/mock-server.js";

const mock = new MockServer();
await mock.start();

const client = new MantyxClient({
  apiKey: "mantyx_test",
  workspaceSlug: "acme",
  baseUrl: mock.baseUrl(),
});

describe("MantyxClient evals", () => {
  it("lists eval datasets", async () => {
    const out = await client.listEvalDatasets();
    expect(out.datasets).toHaveLength(1);
    expect(out.datasets[0]?.id).toBe("ds_demo");
  });

  it("gets an eval dataset with cases", async () => {
    const detail = await client.getEvalDataset("ds_demo");
    expect(detail.cases).toHaveLength(1);
    expect(detail.cases[0]?.name).toBe("hello");
  });

  it("creates and fetches an eval run", async () => {
    const accepted = await client.createEvalRun({
      datasetId: "ds_demo",
      agentId: "agent_1",
    });
    expect(accepted.runId).toMatch(/^eval_/);
    expect(mock.lastEvalCreateBody).toEqual({
      datasetId: "ds_demo",
      agentId: "agent_1",
    });

    const detail = await client.getEvalRun(accepted.runId);
    expect(detail.status).toBe("succeeded");
    expect(detail.passedCases).toBe(1);
  });

  it("validates createEvalRun request shape", async () => {
    await expect(
      client.createEvalRun({ agentId: "a1" } as Parameters<typeof client.createEvalRun>[0]),
    ).rejects.toBeInstanceOf(MantyxError);
  });

  it("streams eval run events", async () => {
    const accepted = await client.createEvalRun({
      datasetId: "ds_demo",
      agentId: "agent_1",
    });
    const events = [];
    for await (const ev of client.streamEvalRun(accepted.runId)) {
      events.push(ev);
    }
    expect(events.some((e) => e.type === "snapshot")).toBe(true);
    expect(events.at(-1)?.type).toBe("run_completed");
  });

  it("runEval blocks until terminal", async () => {
    const detail = await client.runEval(
      { datasetId: "ds_demo", agentId: "agent_1" },
      { pollIntervalMs: 10 },
    );
    expect(detail.status).toBe("succeeded");
  });

  it("compares two eval runs", async () => {
    const a = await client.createEvalRun({ datasetId: "ds_demo", agentId: "agent_1" });
    const b = await client.createEvalRun({
      datasetId: "ds_demo",
      agentId: "agent_1",
      modelId: "platform:demo",
    });
    const compared = await client.compareEvalRuns(a.runId, b.runId);
    expect(compared.runA.id).toBe(a.runId);
    expect(compared.runB.id).toBe(b.runId);
  });

  it("cancels an eval run", async () => {
    const accepted = await client.createEvalRun({
      datasetId: "ds_demo",
      agentId: "agent_1",
    });
    const out = await client.cancelEvalRun(accepted.runId);
    expect(out.ok).toBe(true);
    const detail = await client.getEvalRun(accepted.runId);
    expect(detail.status).toBe("cancelled");
  });
});
