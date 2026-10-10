// Native self-checks for persistent project memory (phases memory1–3, driven
// by scripts/smoke-memory.mjs) and for restart duplicate prevention in gateway
// mode (phase gateway-restart, driven by scripts/smoke-gateway.mjs). They only
// run against throwaway data directories and a throwaway Keychain service.

import { api } from "./api";
import type { AppController } from "./App";
import { proposeEdit } from "./guardian/memory";
import { buildContinuationPrompt } from "./guardian/prompt";
import { StepFn, byTestId, click, list, sleep, typeIntoInput, waitFor } from "./smoke";
import { Checkpoint, ProjectMemory } from "./types";

// Copy of the real checkpoint, placed in the throwaway data dir by the driver.
const LEGACY_ID = "example-cg-v1-legacy-sample";

function selectValue(element: HTMLSelectElement | null, value: string) {
  if (!element) throw new Error("missing select");
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
  setter.call(element, value);
  element.dispatchEvent(new Event("change", { bubbles: true }));
}

function openDetails(id: string) {
  const details = byTestId<HTMLDetailsElement>(id);
  if (!details) throw new Error(`missing ${id}`);
  details.open = true;
}

async function rejects(label: string, action: () => Promise<unknown>, expected: string): Promise<string> {
  try {
    await action();
  } catch (error) {
    const message = String(error);
    if (!message.includes(expected)) throw new Error(`${label}: unexpected error ${message}`);
    return message;
  }
  throw new Error(`${label}: was accepted but should have been refused`);
}

async function openHistory(filter: "project" | "unassigned" | "all") {
  click("nav-history");
  await waitFor("history", () => byTestId("history"));
  selectValue(byTestId<HTMLSelectElement>("history-filter"), filter);
  await sleep(100);
}

async function openCheckpointFromHistory(id: string) {
  const button = await waitFor(`history item ${id}`, () =>
    document.querySelector<HTMLButtonElement>(`[data-testid="history-item"][data-checkpoint-id="${id}"]`)
  );
  button.click();
  await waitFor("editor", () => byTestId("editor"));
}

export async function runMemorySmoke(phase: string, app: AppController, step: StepFn): Promise<string> {
  let prompt = "";
  if (phase === "memory1") {
    let legacyBefore: Checkpoint | null = null;
    let memoryR1: ProjectMemory | null = null;
    let draftId = "";

    await step("legacy checkpoint copy loads as schema 1, unassigned, and is listed under Unassigned", async () => {
      const all = await list();
      legacyBefore = all.find((c) => c.id === LEGACY_ID) ?? null;
      if (!legacyBefore || all.length !== 1) throw new Error(`expected only the legacy checkpoint, found ${all.length}`);
      if (legacyBefore.schemaVersion !== 1 || legacyBefore.projectId) throw new Error("legacy checkpoint not loaded as unassigned schema 1");
      if ((await api.listProjects()).projects.length !== 0) throw new Error("expected no projects");
      await openHistory("project");
      if (document.querySelectorAll('[data-testid="history-item"]').length !== 0) {
        throw new Error("legacy checkpoint matched by name although its name differs from Settings");
      }
      await openHistory("unassigned");
      const items = document.querySelectorAll('[data-testid="history-item"]').length;
      if (items !== 1) throw new Error(`unassigned shows ${items} item(s)`);
      return `${legacyBefore.decisions.length} decisions, ${legacyBefore.completed.length} completed, ${legacyBefore.openTasks.length} open tasks`;
    });

    await step("import shows a preview and creates nothing until approved", async () => {
      await openCheckpointFromHistory(LEGACY_ID);
      click("import-checkpoint");
      await waitFor("proposal", () => byTestId("proposal-changes"));
      const changes = document.querySelectorAll(".change-list li").length;
      if ((await api.listProjects()).projects.length !== 0) throw new Error("project created before approval");
      return `${changes} changes previewed`;
    });

    await step("approving the import creates the project; historical claims stay reported", async () => {
      click("approve-proposal");
      const memory = await waitFor("memory loaded", () => app.memory());
      memoryR1 = memory;
      if (app.activeProjectId() !== memory.projectId) throw new Error("imported project is not active");
      if (memory.revision !== 1 || memory.name !== "Example Project") throw new Error("unexpected memory identity");
      if (memory.workItems.some((w) => w.status === "verified")) throw new Error("import marked something verified");
      const vitest = memory.workItems.find((w) => w.text.includes("[VERIFIED") && w.text.includes("Vitest"));
      if (!vitest || vitest.status !== "reported" || vitest.evidence[0]?.kind !== "prior_report") {
        throw new Error("historical VERIFIED line was not classified as a reported prior claim");
      }
      const awaiting = memory.requiredApprovals.filter((a) => a.status === "awaiting");
      if (awaiting.length !== 2) throw new Error(`expected 2 awaiting approvals, found ${awaiting.length}`);
      if (memory.sources[0]?.checkpointId !== LEGACY_ID) throw new Error("import source not recorded");
      const counts = { reported: 0, pending: 0, failed: 0 } as Record<string, number>;
      for (const item of memory.workItems) counts[item.status] = (counts[item.status] ?? 0) + 1;
      return `project ${memory.projectId.slice(0, 8)}… ${JSON.stringify(counts)}, approvals awaiting=${awaiting.length}, known issues=${memory.knownIssues.length}`;
    });

    await step("the imported checkpoint itself was not modified", async () => {
      const after = await api.getCheckpoint(LEGACY_ID);
      if (!after || !legacyBefore) throw new Error("checkpoint missing");
      if (JSON.stringify(after) !== JSON.stringify(legacyBefore)) throw new Error("checkpoint changed by import");
    });

    await step("project history includes the imported checkpoint by source, not by name", async () => {
      await openHistory("project");
      const items = document.querySelectorAll('[data-testid="history-item"]').length;
      if (items !== 1) throw new Error(`project history shows ${items} item(s)`);
    });

    await step("a new checkpoint belongs to the project by id and is built from memory", async () => {
      click("nav-monitor");
      await waitFor("monitor", () => byTestId("manual-checkpoint"));
      click("manual-checkpoint");
      const draft = await waitFor("project draft", async () => (await list()).find((c) => c.id !== LEGACY_ID) ?? null);
      draftId = draft.id;
      const memory = app.memory()!;
      if (draft.projectId !== memory.projectId || draft.projectName !== memory.name) throw new Error("draft not tied to the project");
      if (draft.schemaVersion !== 2) throw new Error(`schema ${draft.schemaVersion}`);
      if (draft.nextAction !== memory.nextAction?.text) throw new Error("next action not taken from memory");
      const fromMemory = draft.workItems.filter((w) => w.carriedFrom === "memory-r1");
      if (fromMemory.length === 0 || fromMemory.some((w) => w.status !== "pending" && w.status !== "failed")) {
        throw new Error("outstanding memory items not carried as pending/failed");
      }
      if (draft.workItems.some((w) => w.status === "verified")) throw new Error("draft has verified items without evidence");
      await waitFor("editor", () => byTestId("editor"));
      return `${fromMemory.length} outstanding item(s) carried from memory`;
    });

    await step("verified needs evidence from this session (UI and Rust)", async () => {
      openDetails("structured");
      typeIntoInput("new-work-item", "Smoke: memory milestone");
      click("add-work-item");
      const row = await waitFor("new row", () =>
        [...document.querySelectorAll<HTMLElement>('[data-testid="work-item"]')].find((r) => r.textContent?.includes("Smoke: memory milestone"))
      );
      const select = row.querySelector<HTMLSelectElement>('[data-testid="work-item-status"]')!;
      const verifiedOption = [...select.options].find((o) => o.value === "verified")!;
      if (!verifiedOption.disabled) throw new Error("verified selectable without evidence");
      row.querySelector<HTMLButtonElement>('[data-testid="add-evidence"]')!.click();
      await waitFor("evidence form", () => byTestId("evidence-summary"));
      typeIntoInput("evidence-summary", "npx vitest run: 93 passed (smoke run)");
      click("save-evidence");
      await waitFor("verified enabled", () => !verifiedOption.disabled);
      selectValue(select, "verified");
      const saved = await waitFor("autosaved verified", async () => {
        const current = (await list()).find((c) => c.id === draftId);
        const item = current?.workItems.find((w) => w.text === "Smoke: memory milestone");
        return item?.status === "verified" ? { current: current!, item } : null;
      });
      if (saved.item.evidence[0].sessionId !== saved.current.sessionId) throw new Error("evidence not tied to this session");
      const forged = { ...saved.current, workItems: [{ id: "forged", text: "forged", status: "verified" as const, evidence: [] }] };
      const refusal = await rejects("forged verified", () => api.saveCheckpoint(forged), "without supporting evidence");
      return refusal.slice(0, 90);
    });

    await step("a test result run in this session is recorded with provenance", async () => {
      typeIntoInput("test-suite", "Smoke Vitest");
      typeIntoInput("test-command", "npx vitest run");
      typeIntoInput("test-passed", "93");
      click("add-test");
      const test = await waitFor("autosaved test", async () =>
        (await list()).find((c) => c.id === draftId)?.testResults.find((t) => t.suite === "Smoke Vitest")
      );
      if (test.provenance !== "this_session" || test.passed !== 93) throw new Error(JSON.stringify(test));
    });

    await step("merging a final checkpoint into memory needs preview and approval", async () => {
      await sleep(800);
      click("finalize");
      await waitFor("final", async () => (await list()).find((c) => c.id === draftId)?.status === "final");
      await waitFor("merge enabled", () => !byTestId<HTMLButtonElement>("propose-merge")?.disabled);
      click("propose-merge");
      await waitFor("proposal", () => byTestId("proposal-changes"));
      if (app.memory()!.revision !== 1) throw new Error("memory changed before approval");
      const changes = document.querySelectorAll(".change-list li").length;
      click("approve-proposal");
      const memory = await waitFor("revision 2", () => (app.memory()?.revision === 2 ? app.memory() : null));
      const item = memory.workItems.find((w) => w.text === "Smoke: memory milestone");
      if (item?.status !== "verified") throw new Error("verified milestone not in memory");
      if (!memory.testResults.some((t) => t.suite === "Smoke Vitest")) throw new Error("test result not in memory");
      if (!memory.sources.some((s) => s.checkpointId === draftId && s.kind === "merge")) throw new Error("merge source missing");
      return `${changes} changes approved -> revision 2`;
    });

    await step("a stale proposal and an item removal are refused by Rust", async () => {
      const stale = proposeEdit(memoryR1!, { ...memoryR1!, objective: "stale edit" }, new Date());
      const conflict = await rejects("stale", () => api.saveProjectMemory(stale.next, 1), "conflict");
      const current = app.memory()!;
      await rejects("removal", () => api.saveProjectMemory({ ...current, decisions: [] }, current.revision), "would remove");
      const reloaded = await api.getProjectMemory(current.projectId);
      if (reloaded?.revision !== 2 || reloaded.objective !== current.objective) throw new Error("memory changed");
      return conflict.slice(0, 100);
    });

    await step("changing an approval is a reviewed record only", async () => {
      click("nav-project");
      await waitFor("project panel", () => byTestId("edit-memory"));
      click("edit-memory");
      await waitFor("edit form", () => byTestId("memory-edit"));
      selectValue(document.querySelector<HTMLSelectElement>('[data-testid="approval-status"]'), "granted");
      click("review-memory-edit");
      const changes = await waitFor("preview", () => byTestId("proposal-changes")?.textContent);
      if (!changes.includes("awaiting → granted")) throw new Error(`preview: ${changes}`);
      click("approve-proposal");
      const memory = await waitFor("revision 3", () => (app.memory()?.revision === 3 ? app.memory() : null));
      if (memory.requiredApprovals[0].status !== "granted") throw new Error("approval not recorded");
      return `"${memory.requiredApprovals[0].item.slice(0, 50)}…" recorded as granted (nothing executed)`;
    });

    await step("the gateway API key can never be saved into checkpoints, memory or prompts", async () => {
      const secret = `cg-smoke-secret-${Math.random().toString(36).slice(2)}`;
      await api.gatewaySetApiKey(secret);
      try {
        const draft = (await list()).find((c) => c.id === draftId)!;
        const e1 = await rejects("checkpoint", () => api.saveCheckpoint({ ...draft, notes: `key ${secret}` }), "API key");
        const memory = app.memory()!;
        await rejects("memory", () => api.saveProjectMemory({ ...memory, notes: secret }, memory.revision), "API key");
        await rejects("prompt", () => api.checkTextForSecret(`prompt ${secret}`), "API key");
        if (e1.includes(secret)) throw new Error("error message echoed the key");
        if ((await list()).find((c) => c.id === draftId)!.notes.includes(secret)) throw new Error("key reached the checkpoint");
      } finally {
        await api.gatewayClearApiKey();
      }
    });

    await step("copied continuation prompt includes project memory and the next action", async () => {
      await openHistory("project");
      await openCheckpointFromHistory(draftId);
      click("copy-prompt");
      const state = await waitFor("copy state", () => byTestId("copy-state")?.textContent);
      if (!state.startsWith("Copied")) throw new Error(state);
      const saved = (await list()).find((c) => c.id === draftId)!;
      prompt = buildContinuationPrompt(saved, app.memory());
      if (byTestId("prompt-preview")?.textContent !== prompt) throw new Error("preview differs from the generated prompt");
      for (const expected of ["## NEXT ACTION (start here)", "## Project memory — Example Project (revision 3", "### Verified in this checkpoint's session", "Smoke Vitest: PASSED (93 passed) — run in this session"]) {
        if (!prompt.includes(expected)) throw new Error(`prompt missing ${expected}`);
      }
      return `${prompt.length} chars`;
    });

    await step("explicitly assigning the legacy checkpoint upgrades it with a verified backup", async () => {
      await openHistory("unassigned");
      await openCheckpointFromHistory(LEGACY_ID);
      click("assign-project");
      (await waitFor("confirm-assign", () => byTestId("confirm-assign"))).click();
      const after = await waitFor("assigned", async () => {
        const c = await api.getCheckpoint(LEGACY_ID);
        return c?.projectId ? c : null;
      });
      if (after.schemaVersion !== 2 || after.projectId !== app.activeProjectId()) throw new Error("not upgraded/assigned");
      for (const key of Object.keys(legacyBefore!) as (keyof Checkpoint)[]) {
        if (key === "schemaVersion" || key === "updatedAt") continue;
        if (JSON.stringify(after[key]) !== JSON.stringify(legacyBefore![key])) throw new Error(`field ${key} changed`);
      }
    });
  } else if (phase === "memory2") {
    await step("after restart: active project and memory revision 3 are restored", async () => {
      const memory = await waitFor("memory", () => app.memory());
      if (memory.revision !== 3 || app.activeProjectId() !== memory.projectId) throw new Error(`revision ${memory.revision}`);
      if (memory.requiredApprovals[0].status !== "granted") throw new Error("approval change lost");
      if (memory.workItems.find((w) => w.text === "Smoke: memory milestone")?.status !== "verified") throw new Error("milestone lost");
    });

    await step("after restart: project history shows both checkpoints", async () => {
      await openHistory("project");
      const items = document.querySelectorAll('[data-testid="history-item"]').length;
      if (items !== 2) throw new Error(`project history shows ${items}`);
    });

    await step("restoring an earlier revision creates a new revision and keeps the replaced one", async () => {
      click("nav-project");
      await waitFor("panel", () => byTestId("revisions"));
      const details = byTestId<HTMLDetailsElement>("revisions")!;
      details.open = true;
      details.dispatchEvent(new Event("toggle"));
      await waitFor("revision list", () => byTestId("restore-revision"));
      const revisions = await api.listMemoryRevisions(app.activeProjectId());
      if (JSON.stringify(revisions.map((r) => r.revision)) !== "[2,1]") throw new Error(`revisions ${JSON.stringify(revisions)}`);
      click("restore-revision");
      (await waitFor("confirm-restore", () => byTestId("confirm-restore"))).click();
      const memory = await waitFor("revision 4", () => (app.memory()?.revision === 4 ? app.memory() : null));
      if (memory.requiredApprovals[0].status !== "awaiting") throw new Error("revision 2 content not restored");
      const kept = await api.getMemoryRevision(memory.projectId, 3);
      if (kept.requiredApprovals[0].status !== "granted") throw new Error("replaced revision 3 not kept");
      return "restored r2 as r4; r3 kept";
    });
  } else if (phase === "memory3") {
    await step("unreadable memory is reported and never overwritten", async () => {
      await waitFor("settings loaded", () => app.activeProjectId());
      click("nav-project");
      await waitFor("recovery", () => byTestId("memory-recovery"));
      const projectId = app.activeProjectId();
      const listed = await api.listProjects();
      if (listed.problems.length !== 1) throw new Error("problem not listed");
      const valid = await api.getMemoryRevision(projectId, 3);
      await rejects("save over corrupt", () => api.saveProjectMemory(valid, 4), "unreadable");
      return listed.problems[0].error.slice(0, 80) + ` (${projectId.slice(0, 8)}…)`;
    });

    await step("recovering from a revision quarantines the unreadable file", async () => {
      const button = await waitFor("recover button", () => byTestId("recover-revision"));
      button.click();
      (await waitFor("confirm-recover", () => byTestId("confirm-recover"))).click();
      const memory = await waitFor("recovered", () => app.memory());
      if (memory.revision !== 4) throw new Error(`revision ${memory.revision}`);
      if (memory.requiredApprovals[0].status !== "granted") throw new Error("expected revision 3 content");
    });
  }
  return prompt;
}

// Gateway mode, second launch with the same data dir and gateway session,
// whose latest measurement is still above 85%: no second draft may appear.
export async function runGatewayRestartSmoke(app: AppController, step: StepFn) {
  await step("after a restart above 85% in the same gateway session, no duplicate draft is created", async () => {
    const before = (await list()).length;
    await waitFor("measured reading", () => (app.reading().guardPercent ?? 0) >= 85, 20_000);
    await sleep(4000);
    const after = (await list()).length;
    if (before !== 1 || after !== 1) throw new Error(`checkpoints before=${before} after=${after}`);
    const banner = await waitFor("critical banner", () => byTestId("banner-critical"), 10_000);
    return `reading ${app.reading().guardPercent}% · still ${after} draft · ${banner.textContent?.slice(0, 40)}…`;
  });
  await api.smokeSignal("done");
}
