import { useState } from "react";

import { newId } from "../api";
import {
  Checkpoint,
  CommandRecord,
  DecisionRecord,
  ErrorItem,
  EvidenceKind,
  EvidenceStatus,
  TestResult,
  WorkItem
} from "../types";

// The structured (schema 2) parts of a checkpoint. Free-text fields stay in
// CheckpointEditor; these lists hold evidence with explicit status and provenance.
export type Structured = Pick<Checkpoint, "workItems" | "testResults" | "commands" | "errors" | "decisionRecords">;

export function structuredOf(checkpoint: Checkpoint): Structured {
  return {
    workItems: checkpoint.workItems ?? [],
    testResults: checkpoint.testResults ?? [],
    commands: checkpoint.commands ?? [],
    errors: checkpoint.errors ?? [],
    decisionRecords: checkpoint.decisionRecords ?? []
  };
}

// Verified needs evidence of a verifying kind from this checkpoint's session
// (the same rule Rust enforces on save).
export function canVerify(item: WorkItem, sessionId: string): boolean {
  return item.evidence.some((e) => e.kind !== "prior_report" && e.summary.trim() !== "" && e.sessionId === sessionId);
}

const STATUSES: EvidenceStatus[] = ["verified", "reported", "pending", "failed"];
const VERIFYING_KINDS: { kind: EvidenceKind; label: string }[] = [
  { kind: "test_run", label: "Test run" },
  { kind: "command", label: "Command output" },
  { kind: "observation", label: "Observed" },
  { kind: "user_confirmation", label: "User confirmed" }
];

type Props = {
  value: Structured;
  sessionId: string;
  onChange: (next: Structured) => void;
};

function shortId(): string {
  return newId().replace(/-/g, "").slice(0, 16);
}

export function StructuredEditor({ value, sessionId, onChange }: Props) {
  const set = <K extends keyof Structured>(key: K, list: Structured[K]) => onChange({ ...value, [key]: list });
  const count =
    value.workItems.length + value.testResults.length + value.commands.length + value.errors.length + value.decisionRecords.length;

  return (
    <details className="structured" data-testid="structured">
      <summary>
        Evidence &amp; structured record <span className="muted">({count} item{count === 1 ? "" : "s"})</span>
      </summary>
      <WorkItems items={value.workItems} sessionId={sessionId} onChange={(list) => set("workItems", list)} />
      <Tests items={value.testResults} sessionId={sessionId} onChange={(list) => set("testResults", list)} />
      <Commands items={value.commands} onChange={(list) => set("commands", list)} />
      <Errors items={value.errors} onChange={(list) => set("errors", list)} />
      <Decisions items={value.decisionRecords} onChange={(list) => set("decisionRecords", list)} />
    </details>
  );
}

function WorkItems({ items, sessionId, onChange }: { items: WorkItem[]; sessionId: string; onChange: (l: WorkItem[]) => void }) {
  const [text, setText] = useState("");
  const [evidenceFor, setEvidenceFor] = useState<string | null>(null);
  const [kind, setKind] = useState<EvidenceKind>("test_run");
  const [summary, setSummary] = useState("");
  const update = (id: string, patch: Partial<WorkItem>) => onChange(items.map((i) => (i.id === id ? { ...i, ...patch } : i)));

  function addEvidence(item: WorkItem) {
    if (!summary.trim()) return;
    update(item.id, {
      evidence: [...item.evidence, { kind, summary: summary.trim(), observedAt: new Date().toISOString(), sessionId }]
    });
    setSummary("");
    setEvidenceFor(null);
  }

  return (
    <div className="struct-block">
      <strong className="section-title">Work items</strong>
      <p className="muted small">Verified needs evidence recorded in this session. Carried claims are reported until re-verified.</p>
      {items.map((item) => (
        <div key={item.id} className="struct-row" data-testid="work-item">
          <select
            value={item.status}
            onChange={(e) => update(item.id, { status: e.target.value as EvidenceStatus })}
            data-testid="work-item-status"
            className={`status-select status-${item.status}`}
          >
            {STATUSES.map((status) => (
              <option key={status} value={status} disabled={status === "verified" && !canVerify(item, sessionId)}>
                {status}
              </option>
            ))}
          </select>
          <span className="struct-text">
            {item.text}
            <span className="muted small">
              {" "}
              · {item.evidence.length} evidence{item.carriedFrom ? ` · carried from ${item.carriedFrom}` : ""}
            </span>
          </span>
          <button type="button" onClick={() => setEvidenceFor(evidenceFor === item.id ? null : item.id)} data-testid="add-evidence">
            + evidence
          </button>
          <button type="button" className="icon" title="Remove from this checkpoint" onClick={() => onChange(items.filter((i) => i.id !== item.id))}>
            ×
          </button>
          {evidenceFor === item.id ? (
            <div className="struct-add">
              <select value={kind} onChange={(e) => setKind(e.target.value as EvidenceKind)} data-testid="evidence-kind">
                {VERIFYING_KINDS.map((k) => (
                  <option key={k.kind} value={k.kind}>
                    {k.label}
                  </option>
                ))}
              </select>
              <input
                value={summary}
                placeholder="What was observed, e.g. npm test: 93 passed"
                onChange={(e) => setSummary(e.target.value)}
                data-testid="evidence-summary"
              />
              <button type="button" onClick={() => addEvidence(item)} data-testid="save-evidence">
                Add
              </button>
            </div>
          ) : null}
        </div>
      ))}
      <div className="struct-add">
        <input value={text} placeholder="New work item" onChange={(e) => setText(e.target.value)} data-testid="new-work-item" />
        <button
          type="button"
          onClick={() => {
            if (!text.trim()) return;
            onChange([...items, { id: shortId(), text: text.trim(), status: "pending", evidence: [] }]);
            setText("");
          }}
          data-testid="add-work-item"
        >
          Add
        </button>
      </div>
    </div>
  );
}

function Tests({ items, sessionId, onChange }: { items: TestResult[]; sessionId: string; onChange: (l: TestResult[]) => void }) {
  const empty = { suite: "", command: "", covers: "", outcome: "passed" as TestResult["outcome"], passed: "", failed: "", skipped: "" };
  const [form, setForm] = useState(empty);
  const num = (v: string) => (v.trim() === "" ? undefined : Math.max(0, Math.floor(Number(v))));

  function add() {
    if (!form.suite.trim()) return;
    onChange([
      ...items,
      {
        id: shortId(),
        suite: form.suite.trim(),
        command: form.command.trim(),
        covers: form.covers.trim(),
        outcome: form.outcome,
        passed: num(form.passed),
        failed: num(form.failed),
        skipped: num(form.skipped),
        observedAt: new Date().toISOString(),
        sessionId,
        provenance: "this_session"
      }
    ]);
    setForm(empty);
  }

  return (
    <div className="struct-block">
      <strong className="section-title">Test results</strong>
      <p className="muted small">Add only tests actually run in this session. A pass verifies only what the test covers.</p>
      {items.map((test) => (
        <div key={test.id} className="struct-row" data-testid="test-result">
          <span className={`status-chip outcome-${test.outcome}`}>{test.outcome}</span>
          <span className="struct-text">
            {test.suite}
            {test.passed !== undefined ? ` · ${test.passed} passed` : ""}
            {test.failed !== undefined ? ` · ${test.failed} failed` : ""}
            <span className="muted small">
              {" "}
              · {test.provenance === "carried" ? `carried from ${test.carriedFrom}, NOT rerun` : "run this session"}
              {test.covers ? ` · covers: ${test.covers}` : ""}
            </span>
          </span>
          <button type="button" className="icon" title="Remove" onClick={() => onChange(items.filter((t) => t.id !== test.id))}>
            ×
          </button>
        </div>
      ))}
      <div className="struct-add wrap">
        <input value={form.suite} placeholder="Suite, e.g. Vitest" onChange={(e) => setForm({ ...form, suite: e.target.value })} data-testid="test-suite" />
        <input value={form.command} placeholder="Command" onChange={(e) => setForm({ ...form, command: e.target.value })} data-testid="test-command" />
        <input value={form.covers} placeholder="What it covers" onChange={(e) => setForm({ ...form, covers: e.target.value })} />
        <select value={form.outcome} onChange={(e) => setForm({ ...form, outcome: e.target.value as TestResult["outcome"] })} data-testid="test-outcome">
          <option value="passed">passed</option>
          <option value="failed">failed</option>
          <option value="not_run">not run</option>
        </select>
        <input className="num" value={form.passed} placeholder="pass" onChange={(e) => setForm({ ...form, passed: e.target.value })} data-testid="test-passed" />
        <input className="num" value={form.failed} placeholder="fail" onChange={(e) => setForm({ ...form, failed: e.target.value })} />
        <input className="num" value={form.skipped} placeholder="skip" onChange={(e) => setForm({ ...form, skipped: e.target.value })} />
        <button type="button" onClick={add} data-testid="add-test">
          Add
        </button>
      </div>
    </div>
  );
}

function Commands({ items, onChange }: { items: CommandRecord[]; onChange: (l: CommandRecord[]) => void }) {
  const [command, setCommand] = useState("");
  const [purpose, setPurpose] = useState("");
  const [exitCode, setExitCode] = useState("");
  return (
    <div className="struct-block">
      <strong className="section-title">Commands and operations</strong>
      {items.map((c) => (
        <div key={c.id} className="struct-row">
          <code className="struct-text">{c.command}</code>
          <span className="muted small">
            {c.exitCode !== undefined ? `exit ${c.exitCode}` : ""} {c.purpose}
          </span>
          <button type="button" className="icon" title="Remove" onClick={() => onChange(items.filter((x) => x.id !== c.id))}>
            ×
          </button>
        </div>
      ))}
      <div className="struct-add wrap">
        <input value={command} placeholder="Command (no secrets)" onChange={(e) => setCommand(e.target.value)} data-testid="command-text" />
        <input value={purpose} placeholder="Purpose" onChange={(e) => setPurpose(e.target.value)} />
        <input className="num" value={exitCode} placeholder="exit" onChange={(e) => setExitCode(e.target.value)} />
        <button
          type="button"
          onClick={() => {
            if (!command.trim()) return;
            const code = exitCode.trim() === "" ? undefined : Math.trunc(Number(exitCode));
            onChange([
              ...items,
              { id: shortId(), command: command.trim(), purpose: purpose.trim(), exitCode: Number.isFinite(code) ? code : undefined, at: new Date().toISOString() }
            ]);
            setCommand("");
            setPurpose("");
            setExitCode("");
          }}
          data-testid="add-command"
        >
          Add
        </button>
      </div>
    </div>
  );
}

function Errors({ items, onChange }: { items: ErrorItem[]; onChange: (l: ErrorItem[]) => void }) {
  const [error, setError] = useState("");
  const [resolution, setResolution] = useState("");
  return (
    <div className="struct-block">
      <strong className="section-title">Errors and resolutions</strong>
      {items.map((e) => (
        <div key={e.id} className="struct-row">
          <button
            type="button"
            className={`status-chip error-${e.status}`}
            title="Toggle open / resolved"
            onClick={() => onChange(items.map((x) => (x.id === e.id ? { ...x, status: x.status === "open" ? "resolved" : "open" } : x)))}
          >
            {e.status}
          </button>
          <span className="struct-text">
            {e.error}
            {e.resolution ? <span className="muted small"> → {e.resolution}</span> : null}
          </span>
          <button type="button" className="icon" title="Remove" onClick={() => onChange(items.filter((x) => x.id !== e.id))}>
            ×
          </button>
        </div>
      ))}
      <div className="struct-add wrap">
        <input value={error} placeholder="Error" onChange={(e) => setError(e.target.value)} />
        <input value={resolution} placeholder="Resolution (if any)" onChange={(e) => setResolution(e.target.value)} />
        <button
          type="button"
          onClick={() => {
            if (!error.trim()) return;
            onChange([...items, { id: shortId(), error: error.trim(), resolution: resolution.trim(), status: resolution.trim() ? "resolved" : "open" }]);
            setError("");
            setResolution("");
          }}
        >
          Add
        </button>
      </div>
    </div>
  );
}

function Decisions({ items, onChange }: { items: DecisionRecord[]; onChange: (l: DecisionRecord[]) => void }) {
  const [text, setText] = useState("");
  const [rationale, setRationale] = useState("");
  return (
    <div className="struct-block">
      <strong className="section-title">Decisions with rationale</strong>
      {items.map((d) => (
        <div key={d.id} className="struct-row">
          <button
            type="button"
            className={`status-chip decision-${d.status}`}
            title="Toggle active / superseded"
            onClick={() => onChange(items.map((x) => (x.id === d.id ? { ...x, status: x.status === "active" ? "superseded" : "active" } : x)))}
          >
            {d.status}
          </button>
          <span className="struct-text">
            {d.text}
            {d.rationale ? <span className="muted small"> — {d.rationale}</span> : null}
          </span>
          <button type="button" className="icon" title="Remove" onClick={() => onChange(items.filter((x) => x.id !== d.id))}>
            ×
          </button>
        </div>
      ))}
      <div className="struct-add wrap">
        <input value={text} placeholder="Decision" onChange={(e) => setText(e.target.value)} />
        <input value={rationale} placeholder="Rationale" onChange={(e) => setRationale(e.target.value)} />
        <button
          type="button"
          onClick={() => {
            if (!text.trim()) return;
            onChange([
              ...items,
              { id: shortId(), text: text.trim(), rationale: rationale.trim(), decidedAt: new Date().toISOString(), status: "active" }
            ]);
            setText("");
            setRationale("");
          }}
        >
          Add
        </button>
      </div>
    </div>
  );
}
