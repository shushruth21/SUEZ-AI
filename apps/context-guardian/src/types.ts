// "mock" is simulated data. "gateway" is measured by the SUEZ gateway; its
// measurementSource says whether the provider reported the counts or the
// gateway estimated them (stub provider).
export type UsageSourceKind = "mock" | "gateway";

export type MeasurementSource = "simulated" | "provider_reported" | "gateway_estimate";

export type UsageSnapshot = {
  usedTokens: number;
  limitTokens: number;
  percent: number;
  source: UsageSourceKind;
  simulated: boolean;
  capturedAt: string;
  measurementSource?: MeasurementSource;
  cacheStatus?: "miss" | "hit";
  measuredAt?: string;
  model?: string;
  gatewaySessionId?: string;
};

export type CheckpointTrigger = "auto_threshold" | "manual";
export type CheckpointStatus = "draft" | "final";

// Evidence status. "verified" needs evidence observed in the checkpoint's own
// session (enforced in Rust); carried-over claims are at most "reported".
export type EvidenceStatus = "verified" | "reported" | "pending" | "failed";
export type EvidenceKind = "test_run" | "command" | "observation" | "user_confirmation" | "prior_report";

export type Evidence = {
  kind: EvidenceKind;
  summary: string;
  observedAt: string;
  sessionId: string;
};

export type WorkItem = {
  id: string;
  text: string;
  status: EvidenceStatus;
  evidence: Evidence[];
  carriedFrom?: string;
};

export type TestResult = {
  id: string;
  suite: string;
  command: string;
  // What the test covers; a pass verifies only this.
  covers: string;
  outcome: "passed" | "failed" | "not_run";
  passed?: number;
  failed?: number;
  skipped?: number;
  observedAt: string;
  sessionId: string;
  provenance: "this_session" | "carried";
  carriedFrom?: string;
};

export type CommandRecord = { id: string; command: string; purpose: string; exitCode?: number; at: string };
export type ErrorItem = { id: string; error: string; resolution: string; status: "open" | "resolved" };
export type DecisionRecord = {
  id: string;
  text: string;
  rationale: string;
  decidedAt: string;
  status: "active" | "superseded";
};

// Schema 1 files are read as-is (Rust fills the version 2 fields with
// defaults); everything this build saves is schema 2.
export type Checkpoint = {
  schemaVersion: 1 | 2;
  id: string;
  createdAt: string;
  updatedAt: string;
  projectName: string;
  sessionId: string;
  sessionLabel: string;
  trigger: CheckpointTrigger;
  thresholdPercent: number | null;
  status: CheckpointStatus;
  // null when no trustworthy measurement existed when the checkpoint was made.
  usage: UsageSnapshot | null;
  goal: string;
  currentState: string;
  decisions: string[];
  completed: string[];
  openTasks: string[];
  filesTouched: string[];
  nextSteps: string[];
  notes: string;
  // ---- schema 2 ----
  projectId?: string;
  currentTask: string;
  nextAction: string;
  workItems: WorkItem[];
  testResults: TestResult[];
  commands: CommandRecord[];
  errors: ErrorItem[];
  decisionRecords: DecisionRecord[];
  filesCreated: string[];
  filesModified: string[];
};

// ---- Persistent project memory (one record per project, see memory.rs) ----
export type Approval = {
  id: string;
  item: string;
  status: "awaiting" | "granted" | "declined";
  note: string;
  updatedAt: string;
};

export type NextAction = { text: string; setAt: string; sourceCheckpointId?: string };
export type MemorySource = { checkpointId: string; appliedAt: string; kind: "import" | "merge" };

export type ProjectMemory = {
  schemaVersion: 1;
  projectId: string;
  name: string;
  createdAt: string;
  updatedAt: string;
  revision: number;
  objective: string;
  requirements: string[];
  constraints: string[];
  architecture: string;
  environment: string[];
  decisions: DecisionRecord[];
  // verified / reported = completed work and milestones; pending / failed = outstanding.
  workItems: WorkItem[];
  testResults: TestResult[];
  currentStatus: string;
  knownIssues: string[];
  requiredApprovals: Approval[];
  nextAction?: NextAction;
  notes: string;
  sources: MemorySource[];
};

export type ProjectSummary = { projectId: string; name: string; revision: number; updatedAt: string };
export type ProjectList = { projects: ProjectSummary[]; problems: { projectId: string; error: string }[] };
export type RevisionSummary = { revision: number; updatedAt: string };

export type GuardianSettings = {
  projectName: string;
  limitTokens: number;
  checkpointPercent: number;
  criticalPercent: number;
  rearmMargin: number;
  usageSource: UsageSourceKind;
  gatewayUrl: string;
  gatewaySessionId: string;
  pollIntervalSeconds: number;
  // Stable id of the selected project; "" = none (legacy name-only mode).
  activeProjectId: string;
};

export const DEFAULT_SETTINGS: GuardianSettings = {
  projectName: "SUEZ",
  limitTokens: 200_000,
  checkpointPercent: 85,
  criticalPercent: 95,
  rearmMargin: 5,
  usageSource: "mock",
  gatewayUrl: "http://localhost:8080",
  gatewaySessionId: "",
  pollIntervalSeconds: 5,
  activeProjectId: ""
};
