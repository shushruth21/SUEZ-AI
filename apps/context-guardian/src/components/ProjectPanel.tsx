import { useEffect, useState } from "react";

import { linesToList, listToLines } from "../guardian/checkpoint";
import { MemoryProposal, removals, statusGroups } from "../guardian/memory";
import { Approval, ProjectList, ProjectMemory, RevisionSummary } from "../types";

type Props = {
  projects: ProjectList;
  activeProjectId: string;
  memory: ProjectMemory | null;
  memoryError: string | null;
  proposal: MemoryProposal | null;
  message: string;
  onSelect: (projectId: string) => Promise<void>;
  onCreate: (name: string) => Promise<void>;
  onProposeEdit: (edited: ProjectMemory) => void;
  onApprove: () => Promise<void>;
  onCancelProposal: () => void;
  loadRevisions: () => Promise<RevisionSummary[]>;
  onRestore: (revision: number) => Promise<void>;
  onBack: () => void;
};

const PROPOSAL_TITLES: Record<MemoryProposal["kind"], string> = {
  import: "Create project from checkpoint",
  merge: "Update project memory from checkpoint",
  edit: "Save your memory edits",
  restore: "Restore revision"
};

export function ProjectPanel(props: Props) {
  const { projects, activeProjectId, memory, memoryError, proposal } = props;
  const [newName, setNewName] = useState("");
  const [editing, setEditing] = useState(false);

  useEffect(() => setEditing(false), [memory?.revision, activeProjectId]);

  return (
    <section className="project-panel" data-testid="project-panel">
      <div className="history-head">
        <strong>Project memory</strong>
        <button type="button" onClick={props.onBack}>
          Back
        </button>
      </div>
      {props.message ? (
        <p className="info small" data-testid="project-message">
          {props.message}
        </p>
      ) : null}

      {proposal ? (
        <ProposalPreview proposal={proposal} onApprove={props.onApprove} onCancel={props.onCancelProposal} />
      ) : (
        <>
          <label className="field">
            <span>Active project</span>
            <select value={activeProjectId} onChange={(e) => void props.onSelect(e.target.value)} data-testid="project-select">
              <option value="">— none (legacy: checkpoints matched by Settings project name) —</option>
              {projects.projects.map((p) => (
                <option key={p.projectId} value={p.projectId}>
                  {p.name} · {p.projectId.slice(0, 8)} · rev {p.revision}
                </option>
              ))}
            </select>
          </label>
          {projects.problems.length > 0 ? (
            <p className="warning small" data-testid="project-problems">
              Unreadable project memory (left untouched): {projects.problems.map((p) => `${p.projectId}: ${p.error}`).join("; ")}
            </p>
          ) : null}
          <div className="struct-add">
            <input value={newName} placeholder="New project name" onChange={(e) => setNewName(e.target.value)} data-testid="new-project-name" />
            <button
              type="button"
              disabled={!newName.trim()}
              onClick={() => void props.onCreate(newName.trim()).then(() => setNewName(""))}
              data-testid="create-project"
            >
              Create project
            </button>
          </div>
          <p className="muted small">
            Projects are identified by a stable id, not by name. Projects with the same name are never merged. Older
            checkpoints join a project only when you assign or import them.
          </p>

          {memoryError ? (
            <Recovery error={memoryError} loadRevisions={props.loadRevisions} onRestore={props.onRestore} />
          ) : memory ? (
            editing ? (
              <MemoryEditForm memory={memory} onCancel={() => setEditing(false)} onReview={props.onProposeEdit} />
            ) : (
              <>
                <MemorySummary memory={memory} />
                <div className="editor-actions">
                  <button type="button" onClick={() => setEditing(true)} data-testid="edit-memory">
                    Edit memory…
                  </button>
                </div>
                <Revisions memory={memory} loadRevisions={props.loadRevisions} onRestore={props.onRestore} />
              </>
            )
          ) : null}
        </>
      )}
    </section>
  );
}

function ProposalPreview({ proposal, onApprove, onCancel }: { proposal: MemoryProposal; onApprove: () => Promise<void>; onCancel: () => void }) {
  const [busy, setBusy] = useState(false);
  const removed = removals(proposal.changes);
  return (
    <div className="proposal" data-testid="proposal">
      <strong>{PROPOSAL_TITLES[proposal.kind]}</strong>
      <p className="muted small">
        {proposal.baseRevision === null
          ? `Creates “${proposal.next.name}” (new project id ${proposal.next.projectId.slice(0, 8)}…).`
          : `Based on revision ${proposal.baseRevision}. If memory changed since, saving is refused and you review again.`}
        {proposal.sourceCheckpointId ? ` Source checkpoint: ${proposal.sourceCheckpointId} (left unchanged).` : ""}
      </p>
      {proposal.changes.length === 0 ? (
        <p data-testid="proposal-empty">No changes: memory already contains everything from this source.</p>
      ) : (
        <ul className="change-list" data-testid="proposal-changes">
          {proposal.changes.map((change, index) => (
            <li key={index} className={`change change-${change.kind}`}>
              <span className="change-kind">{change.kind}</span> <strong>{change.area}</strong>: {change.summary}
            </li>
          ))}
        </ul>
      )}
      {removed.length > 0 ? (
        <p className="warning small" data-testid="proposal-removals">
          This removes {removed.length} item(s) listed above. Items with ids (decisions, work items, tests, approvals)
          cannot be removed and will be refused; mark them superseded or resolved instead.
        </p>
      ) : null}
      <div className="editor-actions">
        <button
          type="button"
          className="primary"
          disabled={busy || proposal.changes.length === 0}
          onClick={() => {
            setBusy(true);
            void onApprove().finally(() => setBusy(false));
          }}
          data-testid="approve-proposal"
        >
          Approve and save
        </button>
        <button type="button" onClick={onCancel} data-testid="cancel-proposal">
          Cancel
        </button>
      </div>
    </div>
  );
}

function MemorySummary({ memory }: { memory: ProjectMemory }) {
  const groups = statusGroups(memory.workItems);
  const awaiting = memory.requiredApprovals.filter((a) => a.status === "awaiting");
  return (
    <div className="memory-summary" data-testid="memory-summary">
      <p className="muted small">
        {memory.name} · id {memory.projectId} · revision <span data-testid="memory-revision">{memory.revision}</span> · updated{" "}
        {new Date(memory.updatedAt).toLocaleString()}
      </p>
      <p>
        <strong>Next action:</strong> <span data-testid="memory-next-action">{memory.nextAction?.text || "—"}</span>
      </p>
      <p className="small">
        <strong>Objective:</strong> {memory.objective || "—"}
      </p>
      <p className="small" data-testid="memory-counts">
        <span className="status-chip status-verified">{groups.verified.length} verified</span>{" "}
        <span className="status-chip status-reported">{groups.reported.length} reported</span>{" "}
        <span className="status-chip status-failed">{groups.failed.length} failed</span>{" "}
        <span className="status-chip status-pending">{groups.pending.length} pending</span>
      </p>
      {awaiting.length > 0 ? (
        <div className="small" data-testid="memory-approvals">
          <strong>Awaiting your approval</strong> <span className="muted">(tracking only; nothing is executed)</span>
          <ul>
            {awaiting.map((a) => (
              <li key={a.id}>{a.item}</li>
            ))}
          </ul>
        </div>
      ) : null}
      <details className="small">
        <summary>Decisions ({memory.decisions.length}), known issues ({memory.knownIssues.length}), sources ({memory.sources.length})</summary>
        <ul>
          {memory.decisions.map((d) => (
            <li key={d.id} className={d.status === "superseded" ? "muted" : ""}>
              {d.text} {d.status === "superseded" ? "(superseded)" : ""}
            </li>
          ))}
        </ul>
        <ul>
          {memory.knownIssues.map((issue) => (
            <li key={issue}>{issue}</li>
          ))}
        </ul>
        <ul>
          {memory.sources.map((s) => (
            <li key={s.checkpointId}>
              {s.kind}: {s.checkpointId} ({s.appliedAt})
            </li>
          ))}
        </ul>
      </details>
    </div>
  );
}

type EditForm = {
  name: string;
  objective: string;
  currentStatus: string;
  architecture: string;
  notes: string;
  nextAction: string;
  requirements: string;
  constraints: string;
  environment: string;
  knownIssues: string;
};

function MemoryEditForm({ memory, onCancel, onReview }: { memory: ProjectMemory; onCancel: () => void; onReview: (edited: ProjectMemory) => void }) {
  const [form, setForm] = useState<EditForm>({
    name: memory.name,
    objective: memory.objective,
    currentStatus: memory.currentStatus,
    architecture: memory.architecture,
    notes: memory.notes,
    nextAction: memory.nextAction?.text ?? "",
    requirements: listToLines(memory.requirements),
    constraints: listToLines(memory.constraints),
    environment: listToLines(memory.environment),
    knownIssues: listToLines(memory.knownIssues)
  });
  const [approvals, setApprovals] = useState<Approval[]>(memory.requiredApprovals);
  const [newApproval, setNewApproval] = useState("");
  const field = (key: keyof EditForm, label: string, rows = 2) => (
    <label className="field" key={key}>
      <span>{label}</span>
      <textarea rows={rows} value={form[key]} onChange={(e) => setForm({ ...form, [key]: e.target.value })} data-testid={`memory-${key}`} />
    </label>
  );

  function review() {
    const now = new Date().toISOString();
    const nextActionText = form.nextAction.trim();
    const edited: ProjectMemory = {
      ...memory,
      name: form.name.trim() || memory.name,
      objective: form.objective,
      currentStatus: form.currentStatus,
      architecture: form.architecture,
      notes: form.notes,
      requirements: linesToList(form.requirements),
      constraints: linesToList(form.constraints),
      environment: linesToList(form.environment),
      knownIssues: linesToList(form.knownIssues),
      requiredApprovals: approvals,
      nextAction:
        nextActionText === (memory.nextAction?.text ?? "")
          ? memory.nextAction
          : nextActionText
            ? { text: nextActionText, setAt: now }
            : undefined
    };
    onReview(edited);
  }

  return (
    <div className="memory-edit" data-testid="memory-edit">
      {field("name", "Project name (display only; the id stays the same)", 1)}
      {field("objective", "Objective")}
      {field("currentStatus", "Current status", 3)}
      {field("nextAction", "Exact next action")}
      {field("constraints", "Constraints (one per line)", 3)}
      {field("requirements", "Requirements (one per line)")}
      {field("architecture", "Architecture")}
      {field("environment", "Environment facts (one per line, never secrets)")}
      {field("knownIssues", "Known issues (one per line)")}
      {field("notes", "Notes")}
      <strong className="section-title">Required approvals</strong>
      <p className="muted small">Recording a decision here never performs the operation.</p>
      {approvals.map((a) => (
        <div key={a.id} className="struct-row">
          <select
            value={a.status}
            onChange={(e) =>
              setApprovals(approvals.map((x) => (x.id === a.id ? { ...x, status: e.target.value as Approval["status"], updatedAt: new Date().toISOString() } : x)))
            }
            data-testid="approval-status"
          >
            <option value="awaiting">awaiting</option>
            <option value="granted">granted</option>
            <option value="declined">declined</option>
          </select>
          <span className="struct-text small">{a.item}</span>
        </div>
      ))}
      <div className="struct-add">
        <input value={newApproval} placeholder="Operation that needs approval" onChange={(e) => setNewApproval(e.target.value)} />
        <button
          type="button"
          onClick={() => {
            if (!newApproval.trim()) return;
            setApprovals([
              ...approvals,
              { id: crypto.randomUUID().replace(/-/g, "").slice(0, 16), item: newApproval.trim(), status: "awaiting", note: "", updatedAt: new Date().toISOString() }
            ]);
            setNewApproval("");
          }}
        >
          Add
        </button>
      </div>
      <div className="editor-actions">
        <button type="button" className="primary" onClick={review} data-testid="review-memory-edit">
          Review changes…
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  );
}

function Revisions({ memory, loadRevisions, onRestore }: { memory: ProjectMemory; loadRevisions: () => Promise<RevisionSummary[]>; onRestore: (revision: number) => Promise<void> }) {
  const [revisions, setRevisions] = useState<RevisionSummary[] | null>(null);
  const [confirm, setConfirm] = useState<number | null>(null);
  return (
    <details
      className="small"
      onToggle={(e) => {
        if ((e.target as HTMLDetailsElement).open) void loadRevisions().then(setRevisions).catch(() => setRevisions([]));
      }}
      data-testid="revisions"
    >
      <summary>Revision history (latest 50 kept)</summary>
      {revisions === null ? <p className="muted">Loading…</p> : null}
      {revisions?.length === 0 ? <p className="muted">No earlier revisions yet.</p> : null}
      <ul>
        {revisions?.map((r) => (
          <li key={r.revision}>
            Revision {r.revision} · {new Date(r.updatedAt).toLocaleString()}{" "}
            {confirm === r.revision ? (
              <button type="button" onClick={() => void onRestore(r.revision).then(() => setConfirm(null))} data-testid="confirm-restore">
                Confirm restore (current revision {memory.revision} is kept as a revision)
              </button>
            ) : (
              <button type="button" onClick={() => setConfirm(r.revision)} data-testid="restore-revision">
                Restore…
              </button>
            )}
          </li>
        ))}
      </ul>
    </details>
  );
}

function Recovery({ error, loadRevisions, onRestore }: { error: string; loadRevisions: () => Promise<RevisionSummary[]>; onRestore: (revision: number) => Promise<void> }) {
  const [revisions, setRevisions] = useState<RevisionSummary[]>([]);
  const [confirm, setConfirm] = useState<number | null>(null);
  useEffect(() => {
    void loadRevisions().then(setRevisions).catch(() => setRevisions([]));
  }, [loadRevisions]);
  return (
    <div className="recovery" data-testid="memory-recovery">
      <p className="warning">
        This project’s memory file cannot be read: {error}. It has not been changed. You can recover from a saved
        revision; the unreadable file is then kept as <code>memory.corrupt-…json</code>, never deleted.
      </p>
      {revisions.length === 0 ? <p className="muted small">No revisions are available to recover from.</p> : null}
      <ul className="small">
        {revisions.map((r) => (
          <li key={r.revision}>
            Revision {r.revision} · {new Date(r.updatedAt).toLocaleString()}{" "}
            {confirm === r.revision ? (
              <button type="button" onClick={() => void onRestore(r.revision)} data-testid="confirm-recover">
                Confirm recovery
              </button>
            ) : (
              <button type="button" onClick={() => setConfirm(r.revision)} data-testid="recover-revision">
                Recover from this revision…
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
