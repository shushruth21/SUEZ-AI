import { describe, expect, it } from "vitest";

import { createDraft } from "../guardian/checkpoint";
import { Checkpoint } from "../types";
import { GatewayReading, GatewayResult, effectiveThresholds, initialGatewayReading, interpretGatewayResult } from "./gatewayReading";
import { MemoryRequest, ServerMemoryError, createServerMemory, toServerContent } from "./serverCheckpoints";

function localCheckpoint(): Checkpoint {
  const draft = createDraft({
    id: "local-1",
    projectName: "SUEZ",
    sessionId: "sess-1",
    sessionLabel: "Session 1",
    trigger: "manual",
    thresholdPercent: null,
    usage: null,
    now: new Date("2026-11-10T10:00:00Z")
  } as Parameters<typeof createDraft>[0]);
  return {
    ...draft,
    goal: "Ship the gateway hardening",
    currentState: "Tests are green",
    currentTask: "Write the runbook",
    nextAction: "Review the deployment checklist",
    nextSteps: ["Tag the release"],
    completed: ["Added JWT verification"],
    decisions: ["Use JWKS"],
    openTasks: ["Decide on the KMS provider"],
    filesTouched: ["apps/gateway/main.go"],
    filesCreated: ["docs/RUNBOOK.md"],
    filesModified: ["apps/gateway/main.go"],
    workItems: [
      { id: "w1", text: "Policy isolation", status: "verified", evidence: [{ kind: "test_run", summary: "e2e passed 10/10", observedAt: "2026-11-10T09:00:00Z", sessionId: "sess-1" }] },
      { id: "w2", text: "Streaming", status: "reported", evidence: [] },
      { id: "w3", text: "KMS integration", status: "pending", evidence: [] },
      { id: "w4", text: "Load test", status: "failed", evidence: [] }
    ],
    testResults: [
      { id: "t1", suite: "gateway unit", command: "go test ./...", covers: "provider layer", outcome: "passed", passed: 30, failed: 0, observedAt: "2026-11-10T09:00:00Z", sessionId: "sess-1", provenance: "this_session" },
      { id: "t2", suite: "old suite", command: "x", covers: "", outcome: "passed", observedAt: "2026-11-01T09:00:00Z", sessionId: "sess-0", provenance: "carried", carriedFrom: "local-0" }
    ],
    commands: [{ id: "c1", command: "make test", purpose: "full suite", exitCode: 0, at: "2026-11-10T09:00:00Z" }],
    errors: [
      { id: "e1", error: "Flaky stream test", resolution: "", status: "open" },
      { id: "e2", error: "Typo", resolution: "fixed", status: "resolved" }
    ],
    decisionRecords: [
      { id: "d1", text: "Fail closed on JWKS errors", rationale: "security", decidedAt: "2026-11-10T08:00:00Z", status: "active" },
      { id: "d2", text: "Old idea", rationale: "", decidedAt: "2026-11-09T08:00:00Z", status: "superseded" }
    ]
  };
}

describe("toServerContent", () => {
  const content = toServerContent(localCheckpoint());

  it("maps the structured fields", () => {
    expect(content.objective).toBe("Ship the gateway hardening");
    expect(content.currentState).toContain("Tests are green");
    expect(content.currentState).toContain("Current task: Write the runbook");
    expect(content.nextActions).toEqual(["Review the deployment checklist", "Tag the release"]);
    expect(content.decisions).toEqual(["Use JWKS", "Fail closed on JWKS errors — security"]);
    expect(content.artifacts.map((a) => a.reference)).toEqual(["apps/gateway/main.go", "docs/RUNBOOK.md"]);
  });

  it("never upgrades the reliability of a claim", () => {
    expect(content.completedWork).toContain("Policy isolation (verified locally: e2e passed 10/10)");
    expect(content.completedWork).toContain("Streaming (reported, not verified)");
    // Pending and failed work is not listed as completed.
    expect(content.completedWork.join("\n")).not.toContain("KMS integration");
    expect(content.completedWork.join("\n")).not.toContain("Load test");
    expect(content.openQuestions).toContain("KMS integration (pending)");
    expect(content.openQuestions).toContain("Load test (failed)");
    expect(content.toolOutputs.find((line) => line.startsWith("old suite"))).toContain("carried from an earlier session");
    expect(content.risks).toEqual(["Open error: Flaky stream test"]);
  });

  it("does not invent server evidence references", () => {
    expect(content.evidenceRefs).toEqual([]);
    expect(content.assumptions).toEqual([]);
  });

  it("clips oversized text to the server limits", () => {
    const big = toServerContent({ ...localCheckpoint(), goal: "x".repeat(10_000), completed: ["y".repeat(10_000)] });
    expect(big.objective.length).toBeLessThanOrEqual(4000);
    expect(big.completedWork[0].length).toBeLessThanOrEqual(4000);
  });
});

describe("createServerMemory", () => {
  function recorder(responses: Record<string, GatewayResult<unknown>>) {
    const calls: { method: string; path: string; body?: unknown }[] = [];
    const request: MemoryRequest = async (method, path, body) => {
      calls.push({ method, path, body });
      return responses[`${method} ${path}`] ?? { kind: "gateway_error", status: 404, message: "not found" };
    };
    return { calls, memory: createServerMemory(request) };
  }

  it("publishes a local checkpoint as a draft and reviews it separately", async () => {
    const { calls, memory } = recorder({
      "POST /v1/memory/sessions": { kind: "ok", data: { session: { id: "srv-1" } } },
      "POST /v1/memory/sessions/srv-1/checkpoints": { kind: "ok", data: { checkpoint: { id: "cp-1", status: "draft" } } },
      "POST /v1/memory/checkpoints/cp-1/approve": { kind: "ok", data: { checkpoint: { id: "cp-1", status: "approved" } } }
    });
    const session = await memory.ensureSession("sess-1", "Session 1");
    const draft = await memory.publishDraft(session.id, localCheckpoint());
    expect(draft.status).toBe("draft");
    const body = calls[1].body as { content: { objective: string } };
    expect(body.content.objective).toBe("Ship the gateway hardening");
    // Publishing never approves: that is a second, explicit call.
    expect(calls.map((c) => c.path)).not.toContain("/v1/memory/checkpoints/cp-1/approve");
    expect((await memory.review("cp-1", "approve")).status).toBe("approved");
  });

  it("continues from a checkpoint with a token budget", async () => {
    const { calls, memory } = recorder({
      "POST /v1/memory/continuations": { kind: "ok", data: { carriedForward: { tokenBudget: 500, tokensUsed: 120, contextText: "[checkpoint:cp-1] …", evidence: [], omittedEvidenceIds: [] } } }
    });
    const carried = await memory.continueFrom("cp-1", "sess-2", 500);
    expect(carried.tokensUsed).toBeLessThanOrEqual(carried.tokenBudget);
    expect(calls[0].body).toEqual({ checkpointId: "cp-1", externalSessionId: "sess-2", tokenBudget: 500 });
  });

  it("finds the server session by the gateway session id", async () => {
    const { memory } = recorder({
      "GET /v1/memory/sessions": { kind: "ok", data: { sessions: [{ id: "a", externalSessionId: "other" }, { id: "b", externalSessionId: "sess-1" }] } }
    });
    expect(await memory.findSession("sess-1")).toMatchObject({ id: "b" });
    expect(await memory.findSession("missing")).toBeNull();
  });

  it("refuses identifiers that could change the request path", async () => {
    const { calls, memory } = recorder({});
    await expect(memory.getCheckpoint("../../identity")).rejects.toBeInstanceOf(ServerMemoryError);
    await expect(memory.listCheckpoints("a/b")).rejects.toBeInstanceOf(ServerMemoryError);
    await expect(memory.review("x?y=1", "approve")).rejects.toBeInstanceOf(ServerMemoryError);
    expect(calls).toEqual([]);
  });

  it("surfaces server refusals with their status", async () => {
    const { memory } = recorder({
      "POST /v1/memory/checkpoints/cp-1/approve": { kind: "gateway_error", status: 409, message: "only draft checkpoints can be reviewed" }
    });
    await expect(memory.review("cp-1", "approve")).rejects.toMatchObject({ status: 409, kind: "gateway_error" });
    await expect(memory.getCheckpoint("cp-2")).rejects.toMatchObject({ status: 404 });
  });
});

describe("server thresholds", () => {
  const local = { checkpointPercent: 85, criticalPercent: 95, rearmMargin: 5 };
  const okUsage = (extra: object) =>
    ({
      kind: "ok",
      data: {
        sessionId: "s1", status: "ok", message: "", contextWindow: 1000, percent: 62, retrievedAt: "2026-11-10T10:00:00Z",
        measurement: { model: "m", provider: "p", source: "provider_reported", cacheStatus: "miss", promptTokens: 600, completionTokens: 20, contextTokens: 620, measuredAt: "2026-11-10T10:00:00Z" },
        ...extra
      }
    }) as GatewayResult<never>;
  const read = (extra: object): GatewayReading => interpretGatewayResult(okUsage(extra), initialGatewayReading, new Date("2026-11-10T10:00:01Z"), "s1");

  it("uses the tenant's thresholds when the gateway sends them", () => {
    const reading = read({ contextStatus: "checkpoint", thresholds: { noticePercent: 50, checkpointPercent: 60, warningPercent: 70, blockPercent: 90, hardBlock: true } });
    expect(reading.serverStatus).toBe("checkpoint");
    expect(effectiveThresholds(local, reading)).toEqual({ checkpointPercent: 60, criticalPercent: 70, rearmMargin: 5 });
  });

  it("falls back to local settings without them, when they are malformed, or when the reading is stale", () => {
    expect(effectiveThresholds(local, read({}))).toEqual(local);
    expect(effectiveThresholds(local, read({ thresholds: { noticePercent: 90, checkpointPercent: 60, warningPercent: 70, blockPercent: 90, hardBlock: false } }))).toEqual(local);
    const fresh = read({ thresholds: { noticePercent: 50, checkpointPercent: 60, warningPercent: 70, blockPercent: 90, hardBlock: false } });
    expect(effectiveThresholds(local, { ...fresh, stale: true })).toEqual(local);
    expect(effectiveThresholds(local, null)).toEqual(local);
  });
});
