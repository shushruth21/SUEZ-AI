import { Checkpoint } from "../types";

export type HistoryFilter = "project" | "unassigned" | "all";

type Props = {
  checkpoints: Checkpoint[];
  skipped: string[];
  projectName: string;
  // When a project is selected, history is filtered by its stable id.
  activeProject: { id: string; name: string; sourceIds: string[] } | null;
  filter: HistoryFilter;
  onFilter: (filter: HistoryFilter) => void;
  onOpen: (id: string) => void;
  onBack: () => void;
};

function usageLabel(checkpoint: Checkpoint): string {
  const usage = checkpoint.usage;
  if (!usage) return "usage n/a";
  const tag = usage.simulated ? " (mock)" : usage.measurementSource === "gateway_estimate" ? " (gateway est.)" : " (gateway)";
  return `${usage.percent.toFixed(1)}%${tag}`;
}

// Which checkpoints a history view shows. With a project selected, a checkpoint
// belongs to it by projectId, or because it was imported into its memory.
export function visibleCheckpoints(checkpoints: Checkpoint[], props: Pick<Props, "projectName" | "activeProject" | "filter">): Checkpoint[] {
  if (props.filter === "all") return checkpoints;
  if (props.filter === "unassigned") return checkpoints.filter((c) => !c.projectId);
  const active = props.activeProject;
  if (!active) return checkpoints.filter((c) => !c.projectId && c.projectName === props.projectName);
  return checkpoints.filter((c) => c.projectId === active.id || active.sourceIds.includes(c.id));
}

export function HistoryList(props: Props) {
  const visible = visibleCheckpoints(props.checkpoints, props);
  const scope = props.activeProject ? `project “${props.activeProject.name}”` : `“${props.projectName}” (legacy name match)`;

  return (
    <section className="history" data-testid="history">
      <div className="history-head">
        <strong>Checkpoint history</strong>
        <select className="small" value={props.filter} onChange={(e) => props.onFilter(e.target.value as HistoryFilter)} data-testid="history-filter">
          <option value="project">This project</option>
          <option value="unassigned">Unassigned</option>
          <option value="all">All</option>
        </select>
      </div>
      {props.skipped.length > 0 ? (
        <p className="warning small" data-testid="skipped">
          {props.skipped.length} unreadable checkpoint file(s) were skipped and left on disk: {props.skipped.join(", ")}
        </p>
      ) : null}
      {visible.length === 0 ? (
        <p className="muted" data-testid="history-empty">
          No checkpoints {props.filter === "all" ? "yet" : props.filter === "unassigned" ? "without a project" : `yet for ${scope}`}.
        </p>
      ) : (
        <ul className="history-list">
          {visible.map((checkpoint) => (
            <li key={checkpoint.id}>
              <button type="button" className="history-item" onClick={() => props.onOpen(checkpoint.id)} data-testid="history-item" data-checkpoint-id={checkpoint.id}>
                <span className={`status-chip status-${checkpoint.status}`}>
                  {checkpoint.status === "draft" ? "Draft" : "Final"}
                </span>
                <span className="history-main">
                  <span>{checkpoint.goal.trim() || <em className="muted">No goal recorded</em>}</span>
                  <span className="muted small">
                    {new Date(checkpoint.createdAt).toLocaleString()} · {checkpoint.projectName}
                    {checkpoint.projectId ? "" : " (unassigned)"} · {usageLabel(checkpoint)} ·{" "}
                    {checkpoint.trigger === "auto_threshold" ? "auto" : "manual"}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="editor-actions">
        <button type="button" onClick={props.onBack}>
          Back
        </button>
      </div>
    </section>
  );
}
