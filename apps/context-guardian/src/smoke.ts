// End-to-end self-check that runs inside the real native app. It is only
// started when the Rust side was launched with CONTEXT_GUARDIAN_SMOKE_REPORT
// (see scripts/smoke.mjs), always against a throwaway data directory.

import { api } from "./api";
import type { AppController } from "./App";
import { buildContinuationPrompt } from "./guardian/prompt";
import { runGatewayRestartSmoke, runMemorySmoke } from "./smokeMemory";
import { Checkpoint } from "./types";

type Step = { name: string; ok: boolean; detail?: string };
export type StepFn = (name: string, body: () => Promise<string | void>) => Promise<void>;

const SMOKE_GOAL = "Smoke-test goal: verify Context Guardian end to end";

export function byTestId<T extends HTMLElement = HTMLElement>(id: string): T | null {
  return document.querySelector<T>(`[data-testid="${id}"]`);
}

export function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

// Resolves with the first truthy value the probe returns.
export async function waitFor<T>(
  label: string,
  probe: () => T | Promise<T>,
  timeoutMs = 5000
): Promise<NonNullable<T>> {
  const deadline = Date.now() + timeoutMs;
  let last: unknown;
  while (Date.now() < deadline) {
    try {
      const value = await probe();
      if (value) return value as NonNullable<T>;
    } catch (error) {
      last = error;
    }
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${label}${last ? `: ${String(last)}` : ""}`);
}

export function click(id: string) {
  const element = byTestId(id);
  if (!element) throw new Error(`missing element ${id}`);
  element.click();
}

// React tracks input values itself; use the native setter so onChange fires.
export function typeInto(id: string, value: string) {
  const element = byTestId<HTMLTextAreaElement>(id);
  if (!element) throw new Error(`missing element ${id}`);
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  setter.call(element, value);
  element.dispatchEvent(new Event("input", { bubbles: true }));
}

export async function list(): Promise<Checkpoint[]> {
  return (await api.listCheckpoints()).checkpoints;
}

export async function runSmoke(phase: string, app: AppController) {
  const steps: Step[] = [];
  let copiedPrompt = "";

  async function step(name: string, body: () => Promise<string | void>) {
    try {
      const detail = await body();
      steps.push({ name, ok: true, detail: detail || undefined });
    } catch (error) {
      steps.push({ name, ok: false, detail: String(error) });
    }
  }

  if (phase === "1") {
    await step("renders title and MOCK DATA label", async () => {
      const badge = byTestId("mock-badge")?.textContent ?? "";
      const note = byTestId("sim-note")?.textContent ?? "";
      if (badge !== "MOCK DATA") throw new Error(`badge text was ${JSON.stringify(badge)}`);
      if (!note.includes("not real provider telemetry")) throw new Error(`note was ${JSON.stringify(note)}`);
      if (document.title !== "SUEZ Context Guardian") throw new Error(`title was ${document.title}`);
    });

    await step("starts with empty history in the throwaway data dir", async () => {
      const existing = await list();
      if (existing.length !== 0) throw new Error(`expected 0 checkpoints, found ${existing.length}`);
    });

    await step("no checkpoint below threshold (50%, 84%)", async () => {
      app.setPercent(50);
      await sleep(200);
      app.setPercent(84);
      await sleep(400);
      const level = byTestId("level")?.textContent;
      const count = (await list()).length;
      if (count !== 0) throw new Error(`created ${count} checkpoint(s) below threshold`);
      return `level=${level}`;
    });

    await step("85% threshold auto-saves exactly one draft and opens the editor", async () => {
      app.setPercent(86);
      const drafts = await waitFor("auto draft", async () => {
        const all = await list();
        return all.length > 0 ? all : null;
      });
      await waitFor("editor view", () => byTestId("editor"));
      await waitFor("checkpoint banner", () => byTestId("banner-checkpoint"));
      const draft = drafts[0];
      if (drafts.length !== 1) throw new Error(`expected 1 draft, found ${drafts.length}`);
      if (draft.status !== "draft" || draft.trigger !== "auto_threshold" || draft.thresholdPercent !== 85) {
        throw new Error(`unexpected draft fields ${JSON.stringify({ status: draft.status, trigger: draft.trigger, threshold: draft.thresholdPercent })}`);
      }
      if (!draft.usage?.simulated || draft.usage.source !== "mock") throw new Error("draft usage is not labelled mock");
      return `draft=${draft.id} percent=${draft.usage.percent}`;
    });

    await step("staying above 85% does not create duplicates", async () => {
      app.setPercent(88);
      await sleep(300);
      app.setPercent(90);
      await sleep(500);
      const count = (await list()).length;
      if (count !== 1) throw new Error(`expected 1 checkpoint, found ${count}`);
    });

    await step("95% raises the critical warning", async () => {
      app.setPercent(96);
      const banner = await waitFor("critical banner", () => byTestId("banner-critical"));
      const count = (await list()).length;
      if (count !== 1) throw new Error(`critical created extra checkpoints (${count})`);
      return banner.textContent ?? "";
    });

    await step("editing a field autosaves the draft", async () => {
      typeInto("field-goal", SMOKE_GOAL);
      typeInto("field-nextSteps", "- Wire the gateway usage source\n- Add session tagging");
      const saved = await waitFor("autosave", async () => {
        const [first] = await list();
        return first?.goal === SMOKE_GOAL && first.nextSteps.length === 2 ? first : null;
      });
      return `nextSteps=${JSON.stringify(saved.nextSteps)}`;
    });

    await step("Mark final persists status=final", async () => {
      click("finalize");
      await waitFor("final status", async () => (await list())[0]?.status === "final");
      await waitFor("status chip", () => byTestId("status")?.textContent === "Final");
    });

    await step("copy continuation prompt", async () => {
      click("copy-prompt");
      const state = await waitFor("copy state", () => byTestId("copy-state")?.textContent);
      if (!state.startsWith("Copied")) throw new Error(state);
      const [saved] = await list();
      copiedPrompt = buildContinuationPrompt(saved);
      if (!copiedPrompt.includes("SIMULATED (MOCK DATA)")) throw new Error("prompt is missing the simulated label");
      if (!copiedPrompt.includes(SMOKE_GOAL)) throw new Error("prompt is missing the goal");
      const preview = byTestId("prompt-preview")?.textContent ?? "";
      if (preview !== copiedPrompt) throw new Error("preview differs from the saved checkpoint's prompt");
    });

    await step("history lists the checkpoint", async () => {
      click("nav-history");
      await waitFor("history view", () => byTestId("history"));
      const items = document.querySelectorAll('[data-testid="history-item"]').length;
      if (items !== 1) throw new Error(`history shows ${items} item(s)`);
    });

    await step("new session re-arms the trigger and carries fields forward", async () => {
      click("nav-monitor");
      await waitFor("monitor", () => byTestId("new-session"));
      click("new-session");
      await waitFor("usage reset", () => byTestId("percent")?.textContent === "0.0%");
      app.setPercent(91);
      const all = await waitFor("second draft", async () => {
        const current = await list();
        return current.length === 2 ? current : null;
      });
      const [second, first] = all;
      if (second.sessionId === first.sessionId) throw new Error("new draft reused the old session id");
      if (second.goal !== SMOKE_GOAL) throw new Error("goal was not carried forward");
      if (second.status !== "draft") throw new Error("second checkpoint is not a draft");
    });

    await step("window expands for the editor and shrinks back for the monitor", async () => {
      await waitFor("editor view", () => app.view() === "editor");
      await waitFor(`editor size (now ${window.innerWidth}x${window.innerHeight})`, () => window.innerWidth === 440 && window.innerHeight === 680);
      click("nav-monitor");
      await waitFor(`monitor size (now ${window.innerWidth}x${window.innerHeight})`, () => window.innerWidth === 360 && window.innerHeight === 236);
      return "440x680 -> 360x236";
    });
  } else if (phase === "gateway") {
    copiedPrompt = await runGatewaySmoke(app, step);
  } else if (phase === "gateway-restart") {
    await runGatewayRestartSmoke(app, step);
  } else if (phase.startsWith("memory")) {
    copiedPrompt = await runMemorySmoke(phase, app, step);
  } else {
    await step("checkpoints survive an app restart", async () => {
      const all = await list();
      if (all.length !== 2) throw new Error(`expected 2 checkpoints after restart, found ${all.length}`);
      const final = all.find((checkpoint) => checkpoint.status === "final");
      if (!final || final.goal !== SMOKE_GOAL) throw new Error("finalized checkpoint missing after restart");
      return all.map((checkpoint) => `${checkpoint.status}:${checkpoint.id}`).join(", ");
    });

    await step("history view renders persisted checkpoints after restart", async () => {
      click("nav-history");
      await waitFor("history view", () => byTestId("history"));
      const items = document.querySelectorAll('[data-testid="history-item"]').length;
      if (items !== 2) throw new Error(`history shows ${items} item(s)`);
      click("history-item");
      const goal = await waitFor("editor goal", () => byTestId<HTMLTextAreaElement>("field-goal")?.value);
      if (goal !== SMOKE_GOAL) throw new Error(`editor goal was ${JSON.stringify(goal)}`);
    });
  }

  const passed = steps.every((entry) => entry.ok);
  await api.smokeReport({ phase, passed, steps, copiedPrompt, view: app.view() });
}

// The documented demo key of a freshly seeded SUEZ database. The gateway smoke
// only runs against the throwaway test stack started by scripts/smoke-gateway.mjs.
const DEMO_API_KEY = "suez-demo-admin-key";

export function typeIntoInput(id: string, value: string) {
  const element = byTestId<HTMLInputElement>(id);
  if (!element) throw new Error(`missing element ${id}`);
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  setter.call(element, value);
  element.dispatchEvent(new Event("input", { bubbles: true }));
}

function gatewayStatus(): string {
  return byTestId("gateway-status")?.getAttribute("data-status") ?? "";
}

// Gateway phase: the external driver (scripts/smoke-gateway.mjs) waits for
// each "need:*" signal, sends real chat requests through the test gateway with
// this app's session id, then the app checks how it reacted.
async function runGatewaySmoke(
  app: AppController,
  step: (name: string, body: () => Promise<string | void>) => Promise<void>
): Promise<string> {
  const POLL_TIMEOUT = 15_000;
  let prompt = "";
  const expectNoNewCheckpoints = async (count: number, settleMs: number) => {
    await sleep(settleMs);
    const actual = (await list()).length;
    if (actual !== count) throw new Error(`expected ${count} checkpoint(s), found ${actual}`);
  };

  await step("gateway mode shows the GATEWAY source, never MOCK DATA", async () => {
    await waitFor("gateway badge", () => byTestId("source-badge")?.textContent === "GATEWAY");
    if (byTestId("mock-badge")) throw new Error("MOCK DATA badge is visible in gateway mode");
    if (byTestId("sim-note")) throw new Error("simulated-usage note is visible in gateway mode");
    const status = await waitFor("not_configured status", () => gatewayStatus() === "not_configured" && gatewayStatus(), POLL_TIMEOUT);
    if (app.reading().guardPercent !== null) throw new Error("threshold engine received a value without a key");
    return `status=${status}`;
  });

  await step("a wrong API key is reported as an authentication failure", async () => {
    click("nav-settings");
    await waitFor("settings", () => byTestId("setting-apiKey"));
    typeIntoInput("setting-apiKey", "wrong-key-for-smoke-test");
    await waitFor("save enabled", () => !byTestId<HTMLButtonElement>("save-api-key")?.disabled);
    click("save-api-key");
    await waitFor("key stored", () => byTestId("api-key-state")?.textContent?.includes("A key is stored in the macOS Keychain"));
    click("test-connection");
    const result = await waitFor("connection result", () => {
      const text = byTestId("connection-result")?.textContent ?? "";
      return text && text !== "Testing…" ? text : null;
    }, POLL_TIMEOUT);
    if (!result.includes("rejected the API key")) throw new Error(`unexpected connection result: ${result}`);
    click("nav-monitor");
    await waitFor("auth_failed status", () => gatewayStatus() === "auth_failed", POLL_TIMEOUT);
    if (byTestId<HTMLElement>("gateway-status")?.textContent?.includes("wrong-key-for-smoke-test")) {
      throw new Error("API key leaked into the UI");
    }
    return result;
  });

  await step("the correct key is stored in the Keychain and the connection test passes", async () => {
    click("nav-settings");
    await waitFor("settings", () => byTestId("setting-apiKey"));
    typeIntoInput("setting-apiKey", DEMO_API_KEY);
    await waitFor("save enabled", () => !byTestId<HTMLButtonElement>("save-api-key")?.disabled);
    click("save-api-key");
    await waitFor("input cleared", () => byTestId<HTMLInputElement>("setting-apiKey")?.value === "");
    click("test-connection");
    const result = await waitFor("connection result", () => {
      const text = byTestId("connection-result")?.textContent ?? "";
      return text.startsWith("Connected as") ? text : null;
    }, POLL_TIMEOUT);
    click("nav-monitor");
    return result;
  });

  await step("before any tagged request: no measurement, no percentage, no checkpoint", async () => {
    await waitFor("no_measurement status", () => gatewayStatus() === "no_measurement", POLL_TIMEOUT);
    if (byTestId("percent")?.textContent !== "—") throw new Error(`percent shows ${byTestId("percent")?.textContent}`);
    if (app.reading().guardPercent !== null) throw new Error("threshold engine received a value");
    await expectNoNewCheckpoints(0, 0);
  });

  await step("a measured 50% is displayed as a gateway estimate, no checkpoint", async () => {
    await api.smokeSignal("need:50");
    const percent = await waitFor("~50%", () => {
      const value = app.reading().guardPercent;
      return value !== null && value >= 45 && value < 60 ? value : null;
    }, POLL_TIMEOUT);
    await waitFor("estimate badge", () => byTestId("estimate-badge"));
    const note = byTestId("gateway-note")?.textContent ?? "";
    if (!note.includes("gateway estimate (stub model)")) throw new Error(`note was ${note}`);
    await expectNoNewCheckpoints(0, 1500);
    return `percent=${percent}`;
  });

  await step("crossing 85% auto-saves exactly one gateway draft", async () => {
    await api.smokeSignal("need:86");
    const [draft] = await waitFor("auto draft", async () => {
      const all = await list();
      return all.length > 0 ? all : null;
    }, POLL_TIMEOUT);
    await waitFor("editor", () => byTestId("editor"));
    if (draft.trigger !== "auto_threshold" || draft.status !== "draft") throw new Error("draft fields wrong");
    const usage = draft.usage;
    if (!usage || usage.source !== "gateway" || usage.simulated || usage.measurementSource !== "gateway_estimate") {
      throw new Error(`draft usage not labelled as gateway estimate: ${JSON.stringify(usage)}`);
    }
    if (usage.percent < 85) throw new Error(`draft percent ${usage.percent} < 85`);
    if (usage.gatewaySessionId !== draft.sessionId) throw new Error("draft session id does not match the gateway session");
    return `percent=${usage.percent} tokens=${usage.usedTokens}/${usage.limitTokens}`;
  });

  await step("a cached repeat is shown as a cache hit and does not create a duplicate", async () => {
    await api.smokeSignal("need:repeat");
    await waitFor("cache hit", () => app.reading().gateway?.measurement?.cacheStatus === "hit", POLL_TIMEOUT);
    await expectNoNewCheckpoints(1, 3000);
  });

  await step("crossing 95% raises the critical warning without another checkpoint", async () => {
    await api.smokeSignal("need:96");
    const banner = await waitFor("critical banner", () => byTestId("banner-critical"), POLL_TIMEOUT);
    await expectNoNewCheckpoints(1, 1500);
    return banner.textContent ?? "";
  });

  await step("gateway outage: shown as stale/unavailable, stays on GATEWAY, no mock fallback", async () => {
    click("nav-monitor");
    await api.smokeSignal("need:down");
    await waitFor("network_error", () => gatewayStatus() === "network_error", 30_000);
    const state = byTestId("data-state")?.textContent;
    if (state !== "STALE") throw new Error(`data-state badge was ${state}`);
    if (byTestId("source-badge")?.textContent !== "GATEWAY" || byTestId("mock-badge")) {
      throw new Error("source changed during the outage");
    }
    if (app.reading().guardPercent !== null) throw new Error("threshold engine received a stale value");
    if (!byTestId("stale-tag")) throw new Error("last value not marked as not current");
    return byTestId("gateway-status")?.textContent ?? "";
  });

  await step("gateway recovery resumes measured readings", async () => {
    await api.smokeSignal("need:up");
    await waitFor("ok again", () => gatewayStatus() === "ok" && app.reading().guardPercent !== null, 60_000);
    if (byTestId("data-state")) throw new Error("data-state badge still shown");
    await expectNoNewCheckpoints(1, 0);
  });

  await step("continuation prompt states the gateway measurement, not simulation", async () => {
    const [draft] = await list();
    click("nav-history");
    await waitFor("history", () => byTestId("history-item"));
    click("history-item");
    await waitFor("editor", () => byTestId("copy-prompt"));
    click("copy-prompt");
    const state = await waitFor("copy state", () => byTestId("copy-state")?.textContent);
    if (!state.startsWith("Copied")) throw new Error(state);
    prompt = buildContinuationPrompt(draft);
    if (!prompt.includes("measured by the SUEZ gateway from a gateway ESTIMATE (stub model), not provider-reported")) {
      throw new Error("prompt does not describe the gateway estimate");
    }
    if (prompt.includes("SIMULATED")) throw new Error("prompt claims simulated data");
  });

  await api.smokeSignal("done");
  return prompt;
}
