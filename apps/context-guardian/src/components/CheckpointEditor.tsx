import { useEffect, useMemo, useRef, useState } from "react";

import { linesToList, listToLines } from "../guardian/checkpoint";
import { buildContinuationPrompt, describeUsage } from "../guardian/prompt";
import { Checkpoint, ProjectMemory } from "../types";
import { Structured, StructuredEditor, structuredOf } from "./StructuredEditor";

type TextField = "goal" | "currentState" | "notes" | "currentTask" | "nextAction";
type ListField = "decisions" | "completed" | "openTasks" | "filesTouched" | "nextSteps" | "filesCreated" | "filesModified";
type Form = Record<TextField | ListField, string>;

const textFields: { key: TextField; label: string; rows: number }[] = [
  { key: "goal", label: "Goal", rows: 2 },
  { key: "currentState", label: "Current state", rows: 4 },
  { key: "currentTask", label: "Current task", rows: 2 },
  { key: "nextAction", label: "Exact next action", rows: 2 }
];

const listFields: { key: ListField; label: string }[] = [
  { key: "decisions", label: "Decisions made" },
  { key: "completed", label: "Completed" },
  { key: "openTasks", label: "Open tasks" },
  { key: "filesTouched", label: "Files touched" },
  { key: "nextSteps", label: "Next steps" },
  { key: "filesCreated", label: "Files created" },
  { key: "filesModified", label: "Files modified" }
];

function toForm(checkpoint: Checkpoint): Form {
  return {
    goal: checkpoint.goal,
    currentState: checkpoint.currentState,
    notes: checkpoint.notes,
    currentTask: checkpoint.currentTask ?? "",
    nextAction: checkpoint.nextAction ?? "",
    filesCreated: listToLines(checkpoint.filesCreated ?? []),
    filesModified: listToLines(checkpoint.filesModified ?? []),
    decisions: listToLines(checkpoint.decisions),
    completed: listToLines(checkpoint.completed),
    openTasks: listToLines(checkpoint.openTasks),
    filesTouched: listToLines(checkpoint.filesTouched),
    nextSteps: listToLines(checkpoint.nextSteps)
  };
}

function applyForm(checkpoint: Checkpoint, form: Form, structured: Structured): Checkpoint {
  return {
    ...checkpoint,
    ...structured,
    currentTask: form.currentTask,
    nextAction: form.nextAction,
    filesCreated: linesToList(form.filesCreated),
    filesModified: linesToList(form.filesModified),
    goal: form.goal,
    currentState: form.currentState,
    notes: form.notes,
    decisions: linesToList(form.decisions),
    completed: linesToList(form.completed),
    openTasks: linesToList(form.openTasks),
    filesTouched: linesToList(form.filesTouched),
    nextSteps: linesToList(form.nextSteps)
  };
}

type SaveState = { kind: "idle" | "saving" | "saved" | "error"; message?: string };

type Props = {
  checkpoint: Checkpoint;
  // Memory of the selected project (used for the prompt when it matches).
  memory: ProjectMemory | null;
  activeProject: { id: string; name: string } | null;
  onSave: (checkpoint: Checkpoint) => Promise<Checkpoint>;
  onDelete: (id: string) => Promise<void>;
  onCopy: (text: string) => Promise<void>;
  onClose: () => void;
  // Resolves to the stored checkpoint (now carrying projectId), or null on failure.
  onAssign: (checkpoint: Checkpoint) => Promise<Checkpoint | null>;
  onImport: (checkpoint: Checkpoint) => void;
  onProposeMerge: (checkpoint: Checkpoint) => void;
};

const AUTOSAVE_MS = 600;

export function CheckpointEditor(props: Props) {
  const { checkpoint, memory, activeProject, onSave, onDelete, onCopy, onClose } = props;
  const [form, setForm] = useState<Form>(() => toForm(checkpoint));
  const [structured, setStructured] = useState<Structured>(() => structuredOf(checkpoint));
  const [confirmAssign, setConfirmAssign] = useState(false);
  const [base, setBase] = useState(checkpoint);
  const [dirty, setDirty] = useState(false);
  const [saveState, setSaveState] = useState<SaveState>({ kind: "idle" });
  const [copyState, setCopyState] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const formRef = useRef(form);
  formRef.current = form;
  const structuredRef = useRef(structured);
  structuredRef.current = structured;

  // Switching to another checkpoint resets the form.
  useEffect(() => {
    setForm(toForm(checkpoint));
    setStructured(structuredOf(checkpoint));
    setConfirmAssign(false);
    setBase(checkpoint);
    setDirty(false);
    setSaveState({ kind: "idle" });
    setCopyState("");
    setConfirmDelete(false);
  }, [checkpoint.id]);

  const current = useMemo(() => applyForm(base, form, structured), [base, form, structured]);
  const prompt = useMemo(() => buildContinuationPrompt(current, memory), [current, memory]);

  async function persist(next: Checkpoint) {
    setSaveState({ kind: "saving" });
    try {
      const saved = await onSave({ ...next, updatedAt: new Date().toISOString() });
      setBase(saved);
      setDirty(false);
      setSaveState({ kind: "saved", message: new Date().toLocaleTimeString() });
      return saved;
    } catch (error) {
      setSaveState({ kind: "error", message: String(error) });
      throw error;
    }
  }

  // Debounced autosave so drafts are never lost.
  useEffect(() => {
    if (!dirty) return;
    const timer = window.setTimeout(() => {
      void persist(applyForm(base, formRef.current, structuredRef.current)).catch(() => undefined);
    }, AUTOSAVE_MS);
    return () => window.clearTimeout(timer);
  }, [form, structured, dirty]);

  function update(key: keyof Form, value: string) {
    setForm((previous) => ({ ...previous, [key]: value }));
    setDirty(true);
  }

  function updateStructured(next: Structured) {
    setStructured(next);
    setDirty(true);
  }

  const memoryMatches = Boolean(base.projectId && memory && memory.projectId === base.projectId);

  async function finalize() {
    await persist({ ...current, status: "final" }).catch(() => undefined);
  }

  async function reopen() {
    await persist({ ...current, status: "draft" }).catch(() => undefined);
  }

  async function copy() {
    try {
      await onCopy(prompt);
      setCopyState("Copied to clipboard");
    } catch (error) {
      setCopyState(`Copy failed: ${String(error)}`);
    }
  }

  const saveLabel =
    saveState.kind === "saving"
      ? "Saving…"
      : saveState.kind === "saved"
        ? `Saved ${saveState.message}`
        : saveState.kind === "error"
          ? `Save failed: ${saveState.message}`
          : dirty
            ? "Unsaved changes"
            : "Saved";

  return (
    <section className="editor" data-testid="editor">
      <div className="editor-head">
        <div>
          <span className={`status-chip status-${base.status}`} data-testid="status">
            {base.status === "draft" ? "Draft" : "Final"}
          </span>{" "}
          <span className="muted">{new Date(base.createdAt).toLocaleString()}</span>
        </div>
        <span className={`save-state save-${saveState.kind}`} data-testid="save-state">
          {saveLabel}
        </span>
      </div>
      <p className="muted small">
        {base.projectName}
        {base.projectId ? "" : " (no project assigned)"} · {base.sessionLabel} · {base.trigger === "auto_threshold" ? `auto at ${base.thresholdPercent}%` : "manual"}
        <br />
        Usage: {describeUsage(base.usage)}
      </p>

      {textFields.map((field) => (
        <label key={field.key} className="field">
          <span>{field.label}</span>
          <textarea
            rows={field.rows}
            value={form[field.key]}
            data-testid={`field-${field.key}`}
            onChange={(event) => update(field.key, event.target.value)}
          />
        </label>
      ))}
      {listFields.map((field) => (
        <label key={field.key} className="field">
          <span>
            {field.label} <em className="muted">(one per line)</em>
          </span>
          <textarea
            rows={3}
            value={form[field.key]}
            data-testid={`field-${field.key}`}
            onChange={(event) => update(field.key, event.target.value)}
          />
        </label>
      ))}
      <label className="field">
        <span>Notes</span>
        <textarea rows={2} value={form.notes} data-testid="field-notes" onChange={(event) => update("notes", event.target.value)} />
      </label>

      <StructuredEditor value={structured} sessionId={base.sessionId} onChange={updateStructured} />

      <div className="project-actions" data-testid="project-actions">
        {!base.projectId && activeProject ? (
          confirmAssign ? (
            <button
              type="button"
              onClick={() =>
                void props.onAssign(applyForm(base, form, structured)).then((saved) => {
                  setConfirmAssign(false);
                  if (saved) {
                    setBase(saved);
                    setDirty(false);
                  }
                })
              }
              data-testid="confirm-assign"
            >
              Confirm: assign to “{activeProject.name}”
            </button>
          ) : (
            <button type="button" onClick={() => setConfirmAssign(true)} data-testid="assign-project">
              Assign to project “{activeProject.name}”
            </button>
          )
        ) : null}
        {!base.projectId ? (
          <button type="button" onClick={() => props.onImport(current)} data-testid="import-checkpoint">
            Create project from this checkpoint…
          </button>
        ) : null}
        {memoryMatches ? (
          <button
            type="button"
            disabled={base.status !== "final" || dirty}
            title={base.status !== "final" ? "Mark the checkpoint final first" : "Preview the memory update"}
            onClick={() => props.onProposeMerge(current)}
            data-testid="propose-merge"
          >
            Propose project-memory update…
          </button>
        ) : null}
      </div>

      <details className="prompt-preview">
        <summary>Continuation prompt preview</summary>
        <pre data-testid="prompt-preview">{prompt}</pre>
      </details>

      <div className="editor-actions">
        <button type="button" className="primary" onClick={copy} data-testid="copy-prompt">
          Copy continuation prompt
        </button>
        {base.status === "draft" ? (
          <button type="button" onClick={finalize} data-testid="finalize">
            Mark final
          </button>
        ) : (
          <button type="button" onClick={reopen} data-testid="reopen">
            Reopen as draft
          </button>
        )}
        {confirmDelete ? (
          <button type="button" className="danger" onClick={() => void onDelete(base.id)} data-testid="confirm-delete">
            Confirm delete
          </button>
        ) : (
          <button type="button" onClick={() => setConfirmDelete(true)} data-testid="delete">
            Delete
          </button>
        )}
        <button type="button" onClick={onClose}>
          Done
        </button>
      </div>
      {copyState ? (
        <p className="muted small" data-testid="copy-state">
          {copyState}
        </p>
      ) : null}
    </section>
  );
}
