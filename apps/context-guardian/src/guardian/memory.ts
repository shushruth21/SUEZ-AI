// Pure project-memory logic. Nothing here writes to disk: every function
// returns a proposal that the user reviews (see `diffMemory`) before the app
// saves it with a revision check.
//
// Rules:
// - Proposals only add or update items; they never remove any.
// - Historical claims keep their provenance. Importing or carrying a claim
//   never upgrades it: verified claims from an earlier session become
//   "reported" with a prior_report evidence entry that says where they came from.

import {
  Approval,
  Checkpoint,
  DecisionRecord,
  Evidence,
  EvidenceStatus,
  ProjectMemory,
  TestResult,
  WorkItem
} from "../types";

export type MemoryChange = {
  kind: "add" | "update" | "remove";
  area: string;
  summary: string;
};

export type MemoryProposal = {
  kind: "import" | "merge" | "edit" | "restore";
  // Revision the proposal was computed from; null for a new project.
  baseRevision: number | null;
  next: ProjectMemory;
  changes: MemoryChange[];
  sourceCheckpointId?: string;
};

export function newProjectMemory(projectId: string, name: string, now: Date): ProjectMemory {
  const timestamp = now.toISOString();
  return {
    schemaVersion: 1,
    projectId,
    name: name.trim(),
    createdAt: timestamp,
    updatedAt: timestamp,
    revision: 1,
    objective: "",
    requirements: [],
    constraints: [],
    architecture: "",
    environment: [],
    decisions: [],
    workItems: [],
    testResults: [],
    currentStatus: "",
    knownIssues: [],
    requiredApprovals: [],
    notes: "",
    sources: []
  };
}

// ---------------------------------------------------------------------------
// Legacy labels, e.g. "[VERIFIED 2026-10-10, rerun during this checkpoint] …"
// ---------------------------------------------------------------------------

export type LabelClass = "historical_verified" | "reported" | "pending" | "failed" | "limitation" | "approval" | "none";

export type ParsedLabel = { label: string | null; labelClass: LabelClass };

const LABEL_PATTERN = /^\s*\[([^\]]+)\]/;

export function parseLegacyLabel(line: string): ParsedLabel {
  const match = LABEL_PATTERN.exec(line);
  if (!match) return { label: null, labelClass: "none" };
  const label = match[1].trim();
  const upper = label.toUpperCase();
  if (upper.startsWith("VERIFIED")) return { label, labelClass: "historical_verified" };
  if (upper.startsWith("REPORTED") || upper.startsWith("DONE")) return { label, labelClass: "reported" };
  if (upper.startsWith("FAILED")) return { label, labelClass: "failed" };
  if (upper.startsWith("LIMITATION")) return { label, labelClass: "limitation" };
  if (upper.startsWith("PENDING") && upper.includes("USER ACTION")) return { label, labelClass: "approval" };
  if (
    upper.startsWith("PENDING") ||
    upper.startsWith("NOT TESTED") ||
    upper.startsWith("DEFERRED") ||
    upper.startsWith("HOUSEKEEPING")
  ) {
    return { label, labelClass: "pending" };
  }
  return { label, labelClass: "none" };
}

function priorReport(checkpoint: Checkpoint, label: string | null, detail: string): Evidence {
  return {
    kind: "prior_report",
    summary:
      `Imported from checkpoint ${checkpoint.id}${label ? ` (label "${label}")` : ""}; ` +
      `recorded in session ${checkpoint.sessionId} at ${checkpoint.createdAt}. ${detail}`,
    observedAt: checkpoint.createdAt,
    sessionId: checkpoint.sessionId
  };
}

// Verified claims from another session become "reported" with provenance.
function downgradeForImport(item: WorkItem, checkpoint: Checkpoint): WorkItem {
  if (item.status !== "verified") return { ...item, evidence: [...item.evidence] };
  return {
    ...item,
    status: "reported",
    evidence: [
      ...item.evidence,
      priorReport(checkpoint, null, "Verified in that session; not re-verified since.")
    ]
  };
}

function carriedTest(test: TestResult, checkpointId: string): TestResult {
  return test.provenance === "carried" ? { ...test } : { ...test, provenance: "carried", carriedFrom: checkpointId };
}

// Builds the memory for a NEW project from an existing checkpoint (any schema
// version). The checkpoint itself is not modified.
export function importFromCheckpoint(checkpoint: Checkpoint, projectId: string, name: string, now: Date): ProjectMemory {
  const memory = newProjectMemory(projectId, name, now);
  const timestamp = now.toISOString();
  const prefix = `imp-${checkpoint.id}`.slice(0, 40);
  memory.objective = checkpoint.goal;
  memory.currentStatus = checkpoint.currentState;
  memory.notes = checkpoint.notes;

  memory.decisions = checkpoint.decisions.map((text, index) => ({
    id: `${prefix}-d${index + 1}`,
    text,
    rationale: "",
    decidedAt: checkpoint.createdAt,
    status: "active"
  }));
  for (const record of checkpoint.decisionRecords ?? []) {
    memory.decisions.push({ ...record });
  }

  const legacyItem = (text: string, id: string, fallback: EvidenceStatus): WorkItem | null => {
    const { label, labelClass } = parseLegacyLabel(text);
    const status: EvidenceStatus =
      labelClass === "historical_verified" || labelClass === "reported"
        ? "reported"
        : labelClass === "pending"
          ? "pending"
          : labelClass === "failed"
            ? "failed"
            : fallback;
    const detail =
      labelClass === "historical_verified"
        ? "Verified in that session; NOT rerun since. Treat as reported until re-verified."
        : labelClass === "reported"
          ? "Reported in that checkpoint; not verified in the importing session."
          : status === "reported"
            ? "Listed as completed without an evidence label; not verified."
            : "Imported as recorded.";
    return { id, text, status, evidence: [priorReport(checkpoint, label, detail)] };
  };

  checkpoint.completed.forEach((text, index) => {
    const item = legacyItem(text, `${prefix}-c${index + 1}`, "reported");
    if (item) memory.workItems.push(item);
  });
  checkpoint.openTasks.forEach((text, index) => {
    const { labelClass } = parseLegacyLabel(text);
    if (labelClass === "limitation") {
      memory.knownIssues.push(text);
    } else if (labelClass === "approval") {
      memory.requiredApprovals.push({
        id: `${prefix}-a${index + 1}`,
        item: text,
        status: "awaiting",
        note: `Imported from checkpoint ${checkpoint.id}. Recording an approval never performs the operation.`,
        updatedAt: timestamp
      });
    } else {
      const item = legacyItem(text, `${prefix}-o${index + 1}`, "pending");
      if (item) memory.workItems.push(item);
    }
  });
  const [first, ...rest] = checkpoint.nextSteps;
  rest.forEach((text, index) => {
    memory.workItems.push({
      id: `${prefix}-n${index + 2}`,
      text: `Next step: ${text}`,
      status: "pending",
      evidence: [priorReport(checkpoint, null, "Listed as a next step.")]
    });
  });
  const nextActionText = checkpoint.nextAction?.trim() || first;
  if (nextActionText) {
    memory.nextAction = { text: nextActionText, setAt: timestamp, sourceCheckpointId: checkpoint.id };
  }

  for (const item of checkpoint.workItems ?? []) {
    memory.workItems.push(downgradeForImport(item, checkpoint));
  }
  for (const test of checkpoint.testResults ?? []) {
    memory.testResults.push(carriedTest(test, checkpoint.id));
  }
  for (const error of checkpoint.errors ?? []) {
    if (error.status === "open") memory.knownIssues.push(`Open error: ${error.error}`);
  }
  memory.sources = [{ checkpointId: checkpoint.id, appliedAt: timestamp, kind: "import" }];
  return memory;
}

// ---------------------------------------------------------------------------
// Merge a checkpoint into existing memory (additive only)
// ---------------------------------------------------------------------------

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function mergeEvidence(existing: Evidence[], incoming: Evidence[]): Evidence[] {
  const merged = [...existing];
  for (const evidence of incoming) {
    if (!merged.some((e) => sameJson(e, evidence))) merged.push(evidence);
  }
  return merged;
}

function testKey(test: TestResult): string {
  return `test-${test.suite.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")}`.slice(0, 64);
}

export function proposeMerge(memory: ProjectMemory, checkpoint: Checkpoint, now: Date): MemoryProposal {
  const next: ProjectMemory = structuredClone(memory);
  const timestamp = now.toISOString();

  if (!next.objective.trim() && checkpoint.goal.trim()) next.objective = checkpoint.goal;
  if (checkpoint.currentState.trim() && checkpoint.currentState !== next.currentStatus) {
    next.currentStatus = checkpoint.currentState;
  }
  const actionText = checkpoint.nextAction.trim();
  if (actionText && actionText !== next.nextAction?.text) {
    next.nextAction = { text: actionText, setAt: timestamp, sourceCheckpointId: checkpoint.id };
  }

  for (const record of checkpoint.decisionRecords) {
    const index = next.decisions.findIndex((d) => d.id === record.id);
    if (index === -1) next.decisions.push({ ...record });
    else if (!sameJson(next.decisions[index], record)) next.decisions[index] = { ...record };
  }

  for (const item of checkpoint.workItems) {
    const index = next.workItems.findIndex((w) => w.id === item.id);
    if (index === -1) {
      next.workItems.push({ ...item, evidence: [...item.evidence] });
      continue;
    }
    const existing = next.workItems[index];
    const updated: WorkItem = {
      ...existing,
      text: item.text,
      status: item.status,
      evidence: mergeEvidence(existing.evidence, item.evidence)
    };
    if (!sameJson(existing, updated)) next.workItems[index] = updated;
  }

  // Only results observed in the checkpoint's own session update memory;
  // carried copies are old news. Keyed by suite, newest observation wins.
  for (const test of checkpoint.testResults) {
    if (test.provenance !== "this_session") continue;
    const id = testKey(test);
    const incoming = { ...test, id };
    const index = next.testResults.findIndex((t) => t.id === id);
    if (index === -1) next.testResults.push(incoming);
    else if (next.testResults[index].observedAt < test.observedAt) next.testResults[index] = incoming;
  }

  for (const error of checkpoint.errors) {
    const text = `Open error: ${error.error}`;
    if (error.status === "open" && !next.knownIssues.includes(text)) next.knownIssues.push(text);
  }

  const changes = diffMemory(memory, next);
  if (changes.length > 0) {
    if (!next.sources.some((s) => s.checkpointId === checkpoint.id)) {
      next.sources.push({ checkpointId: checkpoint.id, appliedAt: timestamp, kind: "merge" });
      changes.push({ kind: "add", area: "Sources", summary: `checkpoint ${checkpoint.id}` });
    }
    next.updatedAt = timestamp;
  }
  return { kind: "merge", baseRevision: memory.revision, next: changes.length ? next : memory, changes, sourceCheckpointId: checkpoint.id };
}

// ---------------------------------------------------------------------------
// Preview: every difference between two memory records
// ---------------------------------------------------------------------------

function clip(text: string, max = 90): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}

function diffText(changes: MemoryChange[], area: string, before: string, after: string) {
  if (before === after) return;
  if (!before.trim()) changes.push({ kind: "add", area, summary: clip(after) });
  else if (!after.trim()) changes.push({ kind: "remove", area, summary: clip(before) });
  else changes.push({ kind: "update", area, summary: `${clip(before, 40)} → ${clip(after, 60)}` });
}

function diffLines(changes: MemoryChange[], area: string, before: string[], after: string[]) {
  for (const line of after) if (!before.includes(line)) changes.push({ kind: "add", area, summary: clip(line) });
  for (const line of before) if (!after.includes(line)) changes.push({ kind: "remove", area, summary: clip(line) });
}

function diffById<T extends { id: string }>(
  changes: MemoryChange[],
  area: string,
  before: T[],
  after: T[],
  describe: (item: T) => string,
  describeUpdate: (a: T, b: T) => string
) {
  for (const item of after) {
    const old = before.find((b) => b.id === item.id);
    if (!old) changes.push({ kind: "add", area, summary: describe(item) });
    else if (!sameJson(old, item)) changes.push({ kind: "update", area, summary: describeUpdate(old, item) });
  }
  for (const item of before) {
    if (!after.some((a) => a.id === item.id)) changes.push({ kind: "remove", area, summary: describe(item) });
  }
}

export function diffMemory(before: ProjectMemory, after: ProjectMemory): MemoryChange[] {
  const changes: MemoryChange[] = [];
  diffText(changes, "Name", before.name, after.name);
  diffText(changes, "Objective", before.objective, after.objective);
  diffText(changes, "Current status", before.currentStatus, after.currentStatus);
  diffText(changes, "Architecture", before.architecture, after.architecture);
  diffText(changes, "Notes", before.notes, after.notes);
  diffText(changes, "Next action", before.nextAction?.text ?? "", after.nextAction?.text ?? "");
  diffLines(changes, "Requirements", before.requirements, after.requirements);
  diffLines(changes, "Constraints", before.constraints, after.constraints);
  diffLines(changes, "Environment", before.environment, after.environment);
  diffLines(changes, "Known issues", before.knownIssues, after.knownIssues);
  diffById<DecisionRecord>(changes, "Decisions", before.decisions, after.decisions,
    (d) => `${clip(d.text)} [${d.status}]`,
    (a, b) => (a.status !== b.status ? `${clip(b.text, 60)}: ${a.status} → ${b.status}` : `${clip(b.text, 60)} (edited)`));
  diffById<WorkItem>(changes, "Work items", before.workItems, after.workItems,
    (w) => `[${w.status}] ${clip(w.text)}`,
    (a, b) =>
      a.status !== b.status
        ? `${clip(b.text, 60)}: ${a.status} → ${b.status}`
        : a.evidence.length !== b.evidence.length
          ? `${clip(b.text, 60)}: +${b.evidence.length - a.evidence.length} evidence`
          : `${clip(b.text, 60)} (edited)`);
  diffById<TestResult>(changes, "Test results", before.testResults, after.testResults,
    (t) => `${t.suite}: ${t.outcome} (${t.provenance === "carried" ? "carried, not rerun" : `observed ${t.observedAt}`})`,
    (_a, b) => `${b.suite}: now ${b.outcome}, observed ${b.observedAt}`);
  diffById<Approval>(changes, "Required approvals", before.requiredApprovals, after.requiredApprovals,
    (a) => `[${a.status}] ${clip(a.item)}`,
    (a, b) => (a.status !== b.status ? `${clip(b.item, 60)}: ${a.status} → ${b.status}` : `${clip(b.item, 60)} (edited)`));
  const beforeSources = new Set(before.sources.map((s) => s.checkpointId));
  for (const source of after.sources) {
    if (!beforeSources.has(source.checkpointId)) {
      changes.push({ kind: "add", area: "Sources", summary: `checkpoint ${source.checkpointId} (${source.kind})` });
    }
  }
  return changes;
}

// Items that a normal save would drop. Rust refuses these for id'd items too;
// the preview shows them so nothing is removed without the user seeing it.
export function removals(changes: MemoryChange[]): MemoryChange[] {
  return changes.filter((change) => change.kind === "remove");
}

export function proposeEdit(memory: ProjectMemory, edited: ProjectMemory, now: Date): MemoryProposal {
  const next = { ...edited, revision: memory.revision, updatedAt: now.toISOString() };
  return { kind: "edit", baseRevision: memory.revision, next, changes: diffMemory(memory, next) };
}

export function statusGroups(items: WorkItem[]): Record<EvidenceStatus, WorkItem[]> {
  return {
    verified: items.filter((i) => i.status === "verified"),
    reported: items.filter((i) => i.status === "reported"),
    failed: items.filter((i) => i.status === "failed"),
    pending: items.filter((i) => i.status === "pending")
  };
}
