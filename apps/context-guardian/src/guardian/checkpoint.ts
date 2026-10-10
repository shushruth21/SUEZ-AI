import { Checkpoint, CheckpointTrigger, ProjectMemory, TestResult, UsageSnapshot, WorkItem } from "../types";

export type DraftInput = {
  id: string;
  now: Date;
  projectName: string;
  // Stable project id; absent in legacy (name-only) mode.
  projectId?: string;
  sessionId: string;
  sessionLabel: string;
  trigger: CheckpointTrigger;
  thresholdPercent: number | null;
  usage: UsageSnapshot | null;
  previous?: Checkpoint;
  memory?: ProjectMemory | null;
};

// Verification does not carry across sessions: a carried "verified" item
// becomes "reported", with a prior_report entry saying where it came from.
export function carryWorkItem(item: WorkItem, from: Checkpoint): WorkItem {
  const evidence = [...item.evidence];
  let status = item.status;
  if (status === "verified") {
    status = "reported";
    evidence.push({
      kind: "prior_report",
      summary: `Carried from checkpoint ${from.id}: verified in session ${from.sessionId}; not re-verified in this session.`,
      observedAt: from.updatedAt,
      sessionId: from.sessionId
    });
  }
  return { ...item, status, evidence, carriedFrom: from.id };
}

export function carryTestResult(test: TestResult, from: Checkpoint): TestResult {
  return test.provenance === "carried" ? { ...test } : { ...test, provenance: "carried", carriedFrom: from.id };
}

function samePrevious(input: DraftInput): Checkpoint | undefined {
  const previous = input.previous;
  if (!previous) return undefined;
  if (input.projectId) return previous.projectId === input.projectId ? previous : undefined;
  // Legacy mode never inherits from a checkpoint that belongs to a project.
  return !previous.projectId && previous.projectName === input.projectName ? previous : undefined;
}

// A new draft carries forward the long-lived fields of the previous checkpoint
// for the same project and the outstanding work from project memory;
// currentState, notes, commands and file lists start empty because they
// describe this session only.
export function createDraft(input: DraftInput): Checkpoint {
  const previous = samePrevious(input);
  const memory = input.memory && input.memory.projectId === input.projectId ? input.memory : null;
  const timestamp = input.now.toISOString();

  const workItems: WorkItem[] = (previous?.workItems ?? []).map((item) => carryWorkItem(item, previous!));
  if (memory) {
    for (const item of memory.workItems) {
      if ((item.status === "pending" || item.status === "failed") && !workItems.some((w) => w.id === item.id)) {
        workItems.push({ ...item, evidence: [...item.evidence], carriedFrom: `memory-r${memory.revision}` });
      }
    }
  }

  const draft: Checkpoint = {
    schemaVersion: 2,
    id: input.id,
    createdAt: timestamp,
    updatedAt: timestamp,
    projectName: input.projectName,
    sessionId: input.sessionId,
    sessionLabel: input.sessionLabel,
    trigger: input.trigger,
    thresholdPercent: input.thresholdPercent,
    status: "draft",
    usage: input.usage,
    goal: previous?.goal || memory?.objective || "",
    currentState: "",
    decisions: [...(previous?.decisions ?? [])],
    completed: [...(previous?.completed ?? [])],
    openTasks: [...(previous?.openTasks ?? [])],
    filesTouched: [...(previous?.filesTouched ?? [])],
    nextSteps: [...(previous?.nextSteps ?? [])],
    notes: "",
    currentTask: previous?.currentTask ?? "",
    nextAction: previous?.nextAction || memory?.nextAction?.text || "",
    workItems,
    testResults: (previous?.testResults ?? []).map((test) => carryTestResult(test, previous!)),
    commands: [],
    errors: (previous?.errors ?? []).filter((error) => error.status === "open").map((error) => ({ ...error })),
    decisionRecords: (previous?.decisionRecords ?? []).map((record) => ({ ...record })),
    filesCreated: [],
    filesModified: []
  };
  if (input.projectId) draft.projectId = input.projectId;
  return draft;
}

export function linesToList(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.replace(/^\s*(?:[-*•]|\d+[.)])\s+/, "").trim())
    .filter((line) => line.length > 0);
}

export function listToLines(items: string[]): string {
  return items.join("\n");
}

export function newestForProject(checkpoints: Checkpoint[], projectId: string): Checkpoint | undefined {
  return checkpoints
    .filter((checkpoint) => checkpoint.projectId === projectId)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
}

// Legacy (name-only) lookup, used only when no project is selected.
export function newestFor(checkpoints: Checkpoint[], projectName: string): Checkpoint | undefined {
  return checkpoints
    .filter((checkpoint) => !checkpoint.projectId && checkpoint.projectName === projectName)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
}

// True when an automatic threshold checkpoint already exists for this usage
// session. Used to seed the threshold guard after a restart.
export function hasAutoCheckpointForSession(checkpoints: Checkpoint[], sessionId: string): boolean {
  return sessionId !== "" && checkpoints.some((c) => c.sessionId === sessionId && c.trigger === "auto_threshold");
}
