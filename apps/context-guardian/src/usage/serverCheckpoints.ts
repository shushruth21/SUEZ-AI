import { Checkpoint } from "../types";
import { GatewayResult } from "./gatewayReading";

// Client for the governed SUEZ Memory API, reached through the gateway
// (/v1/memory/...). The server is the source of truth for shared checkpoints;
// local checkpoint files stay a private working copy.

export type ServerCheckpointContent = {
  objective: string;
  completedWork: string[];
  decisions: string[];
  currentState: string;
  assumptions: string[];
  openQuestions: string[];
  nextActions: string[];
  artifacts: { label: string; reference: string }[];
  toolOutputs: string[];
  risks: string[];
  evidenceRefs: string[];
};

export type ServerCheckpoint = {
  id: string;
  sessionId: string;
  status: "draft" | "approved" | "rejected" | "superseded";
  origin: "manual" | "generated";
  generated: boolean;
  trigger: "manual" | "threshold";
  contextPercent?: number;
  content?: ServerCheckpointContent;
  editedByReviewer: boolean;
  createdBy: string;
  createdAt: string;
  reviewedBy?: string;
  reviewedAt?: string;
  notice: string;
};

export type CarriedForward = {
  checkpoint: ServerCheckpoint;
  evidence: { id: string; kind: string; origin: string; preview: string; createdAt: string }[];
  omittedEvidenceIds: string[];
  tokenBudget: number;
  tokensUsed: number;
  contextText: string;
  notice: string;
};

export type MemoryRequest = (
  method: "GET" | "POST" | "PATCH",
  path: string,
  body?: unknown
) => Promise<GatewayResult<unknown>>;

export class ServerMemoryError extends Error {
  constructor(
    message: string,
    readonly kind: string,
    readonly status?: number
  ) {
    super(message);
  }
}

function unwrap<T>(result: GatewayResult<unknown>): T {
  if (result.kind === "ok") return result.data as T;
  const status = result.kind === "gateway_error" ? result.status : undefined;
  throw new ServerMemoryError(result.message, result.kind, status);
}

const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;

function segment(value: string): string {
  if (!ID_PATTERN.test(value)) throw new ServerMemoryError("invalid identifier", "invalid_request");
  return value;
}

const SEVERITY_LIMITS = { line: 4000, state: 8000, objective: 4000 };

function clip(text: string, limit: number): string {
  const trimmed = text.trim();
  return trimmed.length <= limit ? trimmed : `${trimmed.slice(0, limit - 1)}…`;
}

function lines(values: (string | null | undefined)[]): string[] {
  return values.map((v) => (v ?? "").trim()).filter(Boolean).map((v) => clip(v, SEVERITY_LIMITS.line));
}

/**
 * Maps a local checkpoint onto the server's structured checkpoint. Nothing is
 * upgraded on the way: locally "verified" items keep the evidence note they
 * were verified with, and items that were only reported, pending or failed
 * are labelled as such. Server evidence references are left empty because
 * local evidence is not a stored server record.
 */
export function toServerContent(checkpoint: Checkpoint): ServerCheckpointContent {
  const done = checkpoint.workItems
    .filter((item) => item.status === "verified" || item.status === "reported")
    .map((item) => {
      const basis = item.evidence[0]?.summary;
      return item.status === "verified" && basis
        ? `${item.text} (verified locally: ${basis})`
        : `${item.text} (reported, not verified)`;
    });
  const outstanding = checkpoint.workItems
    .filter((item) => item.status === "pending" || item.status === "failed")
    .map((item) => `${item.text} (${item.status})`);
  const files = [...new Set([...checkpoint.filesTouched, ...checkpoint.filesCreated, ...checkpoint.filesModified])];
  const tests = checkpoint.testResults.map((test) => {
    const counts = [
      test.passed !== undefined ? `${test.passed} passed` : "",
      test.failed !== undefined ? `${test.failed} failed` : "",
      test.skipped !== undefined ? `${test.skipped} skipped` : ""
    ]
      .filter(Boolean)
      .join(", ");
    const origin = test.provenance === "carried" ? "carried from an earlier session" : "this session";
    return `${test.suite}: ${test.outcome}${counts ? ` (${counts})` : ""} — covers ${test.covers || "unspecified"}; ${origin}`;
  });
  const commands = checkpoint.commands.map(
    (c) => `$ ${c.command}${c.exitCode !== undefined ? ` (exit ${c.exitCode})` : ""}${c.purpose ? ` — ${c.purpose}` : ""}`
  );
  const decisions = [
    ...checkpoint.decisions,
    ...checkpoint.decisionRecords
      .filter((d) => d.status === "active")
      .map((d) => (d.rationale ? `${d.text} — ${d.rationale}` : d.text))
  ];
  const openErrors = checkpoint.errors.filter((e) => e.status === "open").map((e) => e.error);
  return {
    objective: clip(checkpoint.goal, SEVERITY_LIMITS.objective),
    completedWork: lines([...checkpoint.completed, ...done]),
    decisions: lines(decisions),
    currentState: clip([checkpoint.currentState, checkpoint.currentTask ? `Current task: ${checkpoint.currentTask}` : ""].filter(Boolean).join("\n"), SEVERITY_LIMITS.state),
    assumptions: [],
    openQuestions: lines([...checkpoint.openTasks, ...outstanding]),
    nextActions: lines([checkpoint.nextAction, ...checkpoint.nextSteps]),
    artifacts: files.slice(0, 100).map((file) => ({ label: "file", reference: clip(file, 2000) })),
    toolOutputs: lines([...tests, ...commands]).slice(0, 100),
    risks: lines(openErrors.map((error) => `Open error: ${error}`)),
    evidenceRefs: []
  };
}

export function createServerMemory(request: MemoryRequest) {
  return {
    /** Server session for a gateway session id, or null when nothing was captured or created yet. */
    async findSession(externalSessionId: string): Promise<{ id: string } | null> {
      const data = unwrap<{ sessions?: { id: string; externalSessionId: string }[] }>(
        await request("GET", `/v1/memory/sessions`)
      );
      return data.sessions?.find((s) => s.externalSessionId === externalSessionId) ?? null;
    },
    async ensureSession(externalSessionId: string, title: string): Promise<{ id: string }> {
      const data = unwrap<{ session: { id: string } }>(
        await request("POST", `/v1/memory/sessions`, { externalSessionId: segment(externalSessionId), title })
      );
      return data.session;
    },
    async listCheckpoints(serverSessionId: string): Promise<ServerCheckpoint[]> {
      const data = unwrap<{ checkpoints?: ServerCheckpoint[] }>(
        await request("GET", `/v1/memory/sessions/${segment(serverSessionId)}/checkpoints`)
      );
      return data.checkpoints ?? [];
    },
    async getCheckpoint(id: string): Promise<ServerCheckpoint> {
      return unwrap<{ checkpoint: ServerCheckpoint }>(await request("GET", `/v1/memory/checkpoints/${segment(id)}`)).checkpoint;
    },
    /** Uploads a local checkpoint as a server draft. It still needs approval on the server. */
    async publishDraft(serverSessionId: string, checkpoint: Checkpoint): Promise<ServerCheckpoint> {
      return unwrap<{ checkpoint: ServerCheckpoint }>(
        await request("POST", `/v1/memory/sessions/${segment(serverSessionId)}/checkpoints`, {
          content: toServerContent(checkpoint),
          contextPercent: checkpoint.usage?.percent
        })
      ).checkpoint;
    },
    async review(id: string, decision: "approve" | "reject"): Promise<ServerCheckpoint> {
      return unwrap<{ checkpoint: ServerCheckpoint }>(
        await request("POST", `/v1/memory/checkpoints/${segment(id)}/${decision}`)
      ).checkpoint;
    },
    /** Starts a new session from an approved checkpoint; returns exactly what was carried forward. */
    async continueFrom(checkpointId: string, newExternalSessionId: string, tokenBudget: number): Promise<CarriedForward> {
      const data = unwrap<{ carriedForward: CarriedForward }>(
        await request("POST", `/v1/memory/continuations`, {
          checkpointId: segment(checkpointId),
          externalSessionId: segment(newExternalSessionId),
          tokenBudget
        })
      );
      return data.carriedForward;
    }
  };
}

export type ServerMemory = ReturnType<typeof createServerMemory>;
