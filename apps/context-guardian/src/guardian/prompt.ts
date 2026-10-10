import { Checkpoint, ProjectMemory, TestResult, UsageSnapshot, WorkItem } from "../types";

const NONE = "_None recorded._";

export function formatTokens(value: number): string {
  return Math.round(value).toLocaleString("en-US");
}

export function describeUsage(usage: UsageSnapshot | null): string {
  if (usage === null) {
    return "unavailable — no trustworthy context measurement existed when this checkpoint was made.";
  }
  const base = `${formatTokens(usage.usedTokens)} / ${formatTokens(usage.limitTokens)} tokens (${usage.percent.toFixed(1)}%)`;
  if (usage.simulated || usage.source === "mock") {
    return `${base} — SIMULATED (MOCK DATA). This figure is not real provider telemetry.`;
  }
  const origin =
    usage.measurementSource === "provider_reported"
      ? "provider-reported token counts"
      : usage.measurementSource === "gateway_estimate"
        ? "a gateway ESTIMATE (stub model), not provider-reported"
        : "an unspecified measurement";
  const details = [
    `measured by the SUEZ gateway from ${origin}`,
    usage.model ? `model ${usage.model}` : "",
    usage.cacheStatus === "hit" ? "latest request served from cache" : "",
    usage.measuredAt ? `measured at ${usage.measuredAt}` : ""
  ].filter(Boolean);
  return `${base} — ${details.join("; ")}.`;
}

function describeTrigger(checkpoint: Checkpoint): string {
  if (checkpoint.trigger === "auto_threshold") {
    const threshold = checkpoint.thresholdPercent ?? "?";
    return `automatic, at the ${threshold}% context threshold`;
  }
  return "manual";
}

function section(title: string, body: string, level = 2): string {
  return `${"#".repeat(level)} ${title}\n${body.trim() ? body.trim() : NONE}`;
}

function listSection(title: string, items: string[], level = 2): string {
  const clean = items.map((item) => item.trim()).filter(Boolean);
  return section(title, clean.map((item) => `- ${item}`).join("\n"), level);
}

// Lists in the prompt are capped so the prompt stays focused; the rest stays
// in project memory and is referenced, not pasted.
export const PROMPT_LIST_LIMIT = 12;

function capped(items: string[], limit = PROMPT_LIST_LIMIT): string[] {
  if (items.length <= limit) return items;
  return [...items.slice(0, limit), `…and ${items.length - limit} more (in project memory / the checkpoint; not included here)`];
}

function cappedSection(title: string, items: string[], level = 2): string {
  return listSection(title, capped(items), level);
}

export function hasStructuredContent(checkpoint: Checkpoint): boolean {
  return Boolean(
    checkpoint.currentTask?.trim() ||
      checkpoint.nextAction?.trim() ||
      checkpoint.workItems?.length ||
      checkpoint.testResults?.length ||
      checkpoint.commands?.length ||
      checkpoint.errors?.length ||
      checkpoint.decisionRecords?.length ||
      checkpoint.filesCreated?.length ||
      checkpoint.filesModified?.length
  );
}

// The single next action: the checkpoint's own, else memory's, else the first next step.
export function resolveNextAction(checkpoint: Checkpoint, memory: ProjectMemory | null): string {
  return checkpoint.nextAction?.trim() || memory?.nextAction?.text.trim() || checkpoint.nextSteps[0]?.trim() || "";
}

function evidenceNote(item: WorkItem): string {
  const latest = item.evidence[item.evidence.length - 1];
  return latest ? ` — evidence: ${latest.summary} (${latest.kind}, ${latest.observedAt})` : "";
}

function describeTest(test: TestResult): string {
  const counts = [
    test.passed !== undefined ? `${test.passed} passed` : "",
    test.failed !== undefined ? `${test.failed} failed` : "",
    test.skipped !== undefined ? `${test.skipped} skipped` : ""
  ].filter(Boolean);
  const origin =
    test.provenance === "this_session"
      ? `run in this session at ${test.observedAt}`
      : `CARRIED from checkpoint ${test.carriedFrom ?? "?"}, observed ${test.observedAt}; NOT rerun`;
  return (
    `${test.suite}: ${test.outcome.toUpperCase()}${counts.length ? ` (${counts.join(", ")})` : ""} — ${origin}` +
    (test.command ? `; command: ${test.command}` : "") +
    (test.covers ? `; covers only: ${test.covers}` : "")
  );
}

function structuredSections(checkpoint: Checkpoint): string[] {
  const items = checkpoint.workItems ?? [];
  const by = (status: WorkItem["status"]) => items.filter((item) => item.status === status);
  return [
    section("Current task", checkpoint.currentTask ?? ""),
    "",
    "## Work by evidence status",
    "",
    cappedSection(
      "Verified in this checkpoint's session (evidence recorded)",
      by("verified").map((item) => `${item.text}${evidenceNote(item)}`),
      3
    ),
    "",
    cappedSection(
      "Reported — NOT verified in this checkpoint's session",
      by("reported").map((item) => `${item.text}${item.carriedFrom ? ` (carried from ${item.carriedFrom})` : ""}`),
      3
    ),
    "",
    cappedSection("Failed", by("failed").map((item) => `${item.text}${evidenceNote(item)}`), 3),
    "",
    cappedSection("Pending", by("pending").map((item) => item.text), 3),
    "",
    cappedSection("Test results", (checkpoint.testResults ?? []).map(describeTest)),
    "",
    cappedSection(
      "Commands run in this session",
      (checkpoint.commands ?? []).map(
        (c) => `\`${c.command}\`${c.exitCode !== undefined ? ` (exit ${c.exitCode})` : ""}${c.purpose ? ` — ${c.purpose}` : ""}`
      )
    ),
    "",
    cappedSection(
      "Errors and resolutions",
      (checkpoint.errors ?? []).map((e) => `[${e.status}] ${e.error}${e.resolution ? ` → ${e.resolution}` : ""}`)
    ),
    "",
    cappedSection(
      "Decisions with rationale",
      (checkpoint.decisionRecords ?? []).filter((d) => d.status === "active").map((d) => `${d.text}${d.rationale ? ` — because ${d.rationale}` : ""}`)
    ),
    "",
    cappedSection("Files created", checkpoint.filesCreated ?? []),
    "",
    cappedSection("Files modified", checkpoint.filesModified ?? []),
    ""
  ];
}

function memorySections(memory: ProjectMemory, checkpoint: Checkpoint): string[] {
  const inCheckpoint = new Set((checkpoint.workItems ?? []).map((item) => item.id));
  const others = memory.workItems.filter((item) => !inCheckpoint.has(item.id));
  const awaiting = memory.requiredApprovals.filter((a) => a.status === "awaiting");
  return [
    `## Project memory — ${memory.name} (revision ${memory.revision}, updated ${memory.updatedAt})`,
    "Persistent memory for this project, independent of this checkpoint.",
    "",
    section("Objective", memory.objective),
    "",
    section("Project status", memory.currentStatus === checkpoint.currentState ? "(same as Current state above)" : memory.currentStatus),
    "",
    cappedSection("Constraints", memory.constraints),
    "",
    cappedSection(
      "Active decisions",
      memory.decisions.filter((d) => d.status === "active").map((d) => `${d.text}${d.rationale ? ` — because ${d.rationale}` : ""}`)
    ),
    "",
    cappedSection(
      "Required approvals — AWAITING (do not perform these without explicit user approval)",
      awaiting.map((a) => a.item)
    ),
    "",
    cappedSection("Known issues and limitations", memory.knownIssues),
    "",
    cappedSection(
      "Completed per memory — verified when recorded",
      others.filter((i) => i.status === "verified").map((i) => `${i.text}${evidenceNote(i)}`)
    ),
    "",
    cappedSection(
      "Completed per memory — reported, NOT verified (re-verify before relying on them)",
      others.filter((i) => i.status === "reported").map((i) => i.text)
    ),
    "",
    cappedSection("Outstanding per memory", others.filter((i) => i.status === "pending" || i.status === "failed").map((i) => `[${i.status}] ${i.text}`)),
    ""
  ];
}

export function buildContinuationPrompt(checkpoint: Checkpoint, memory: ProjectMemory | null = null): string {
  const status =
    checkpoint.status === "draft"
      ? "draft (auto-saved; not yet reviewed by the user)"
      : "final (reviewed by the user)";

  const relevantMemory = memory && checkpoint.projectId === memory.projectId ? memory : null;
  const extended = relevantMemory !== null || hasStructuredContent(checkpoint);
  const nextAction = resolveNextAction(checkpoint, relevantMemory);
  const firstStep = checkpoint.nextSteps.length > 0 ? '"Next steps"' : '"Open tasks"';

  const header = [
    "# Continuation prompt — SUEZ Context Guardian checkpoint",
    "",
    `You are continuing work on the project "${checkpoint.projectName}" from a previous AI session ` +
      "that was checkpointed before its context window filled up. Treat the checkpoint below as the " +
      "authoritative record of project state.",
    "",
    "## Checkpoint",
    `- Checkpoint ID: ${checkpoint.id}`,
    ...(checkpoint.projectId ? [`- Project ID: ${checkpoint.projectId}`] : []),
    `- Created: ${checkpoint.createdAt}`,
    `- Session: ${checkpoint.sessionLabel || checkpoint.sessionId}`,
    `- Status: ${status}`,
    `- Trigger: ${describeTrigger(checkpoint)}`,
    `- Context usage at checkpoint: ${describeUsage(checkpoint.usage)}`,
    ""
  ];
  const legacy = [
    section("Goal", checkpoint.goal),
    "",
    section("Current state", checkpoint.currentState),
    "",
    listSection("Decisions made", checkpoint.decisions),
    "",
    listSection("Completed", checkpoint.completed),
    "",
    listSection("Open tasks", checkpoint.openTasks),
    "",
    listSection("Files touched", checkpoint.filesTouched),
    "",
    listSection("Next steps", checkpoint.nextSteps),
    "",
    section("Notes", checkpoint.notes),
    ""
  ];

  if (!extended) {
    return [
      ...header,
      ...legacy,
      "## Instructions",
      "1. Read this whole checkpoint before acting.",
      '2. Do not redo anything listed under "Completed" unless asked.',
      "3. Keep to the decisions listed above; if one has to change, say so explicitly and explain why.",
      `4. Confirm your understanding in one short paragraph, then start with the first item under ${firstStep}.`
    ].join("\n");
  }

  return [
    ...header,
    section("NEXT ACTION (start here)", nextAction),
    "",
    ...legacy,
    ...structuredSections(checkpoint),
    ...(relevantMemory ? memorySections(relevantMemory, checkpoint) : []),
    "## Instructions",
    "1. Read this whole prompt before acting.",
    "2. Only items under a \"Verified\" heading have evidence recorded in their session. Everything marked reported or carried is a claim: re-verify it before relying on it, and never describe it as rerun.",
    "3. A passing test verifies only what that test covers.",
    "4. Keep to the active decisions; if one has to change, say so explicitly and explain why.",
    "5. Never perform an item under \"Required approvals — AWAITING\" without the user's explicit approval.",
    "6. Confirm your understanding in one short paragraph, then start with the NEXT ACTION."
  ].join("\n");
}
