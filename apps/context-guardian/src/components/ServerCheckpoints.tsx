import { useCallback, useEffect, useState } from "react";

import { Checkpoint } from "../types";
import { ServerContextStatus } from "../usage/gatewayReading";
import { CarriedForward, ServerCheckpoint, ServerMemory, ServerMemoryError } from "../usage/serverCheckpoints";

type Props = {
  memory: ServerMemory;
  gatewaySessionId: string;
  sessionLabel: string;
  serverStatus: ServerContextStatus | null;
  // Most recent final local checkpoint of this session, if any.
  localFinal: Checkpoint | null;
  onContinue: (newSessionId: string, carried: CarriedForward) => void;
  newSessionId: () => string;
};

const CONTINUATION_TOKEN_BUDGET = 2000;

function describe(error: unknown): string {
  if (error instanceof ServerMemoryError) {
    if (error.status === 403) return `The server refused this: ${error.message}`;
    if (error.status === 404) return "Not found on the server (it may have been deleted).";
    return error.message;
  }
  return error instanceof Error ? error.message : "Request failed.";
}

/**
 * Checkpoints stored in SUEZ for the current gateway session. The server is
 * the shared record: drafts made at the tenant's threshold show up here, and
 * nothing is carried into a new session until a person has approved it.
 */
export function ServerCheckpoints({ memory, gatewaySessionId, sessionLabel, serverStatus, localFinal, onContinue, newSessionId }: Props) {
  const [serverSessionId, setServerSessionId] = useState<string | null>(null);
  const [checkpoints, setCheckpoints] = useState<ServerCheckpoint[]>([]);
  const [open, setOpen] = useState<ServerCheckpoint | null>(null);
  const [message, setMessage] = useState<string>("");
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const session = await memory.findSession(gatewaySessionId);
      setServerSessionId(session?.id ?? null);
      setCheckpoints(session ? await memory.listCheckpoints(session.id) : []);
      setMessage("");
    } catch (error) {
      setMessage(describe(error));
    }
  }, [memory, gatewaySessionId]);

  useEffect(() => {
    setOpen(null);
    void refresh();
  }, [refresh, serverStatus]);

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try {
      await action();
      await refresh();
    } catch (error) {
      setMessage(describe(error));
    } finally {
      setBusy(false);
    }
  };

  const publish = () =>
    run(async () => {
      if (!localFinal) return;
      const session = serverSessionId ? { id: serverSessionId } : await memory.ensureSession(gatewaySessionId, sessionLabel);
      const draft = await memory.publishDraft(session.id, localFinal);
      setOpen(await memory.getCheckpoint(draft.id));
      setMessage("Uploaded as a draft. Review it below, then approve it.");
    });

  return (
    <section className="server-checkpoints" data-testid="server-checkpoints">
      <div className="server-checkpoints-head">
        <strong>SUEZ checkpoints</strong>
        {serverStatus ? (
          <span className={`server-status status-${serverStatus}`} data-testid="server-status" title="Judged by the gateway against your organisation's thresholds">
            {serverStatus}
          </span>
        ) : null}
        <span className="spacer" />
        <button type="button" disabled={busy} onClick={() => void refresh()} data-testid="server-refresh">
          Refresh
        </button>
        <button type="button" disabled={busy || !localFinal} onClick={() => void publish()} title={localFinal ? "Upload the latest final local checkpoint as a server draft" : "Finalise a local checkpoint first"} data-testid="server-publish">
          Upload latest
        </button>
      </div>
      {message ? (
        <p className="muted small" data-testid="server-message">
          {message}
        </p>
      ) : null}
      {checkpoints.length === 0 ? (
        <p className="muted small">No checkpoints are stored in SUEZ for this session.</p>
      ) : (
        <ul className="server-checkpoint-list">
          {checkpoints.map((checkpoint) => (
            <li key={checkpoint.id} data-testid={`server-checkpoint-${checkpoint.status}`}>
              <span className={`pill pill-${checkpoint.status}`}>{checkpoint.status}</span>
              <span>{checkpoint.generated ? "auto-drafted" : "written by a person"}</span>
              <span className="muted small">{new Date(checkpoint.createdAt).toLocaleString()}</span>
              <span className="spacer" />
              <button type="button" disabled={busy} onClick={() => void run(async () => setOpen(await memory.getCheckpoint(checkpoint.id)))}>
                Review
              </button>
            </li>
          ))}
        </ul>
      )}
      {open ? (
        <div className="server-checkpoint-detail" data-testid="server-checkpoint-detail">
          <p className="notice small">{open.notice}</p>
          {open.content ? (
            <dl>
              <dt>Objective</dt>
              <dd>{open.content.objective || "—"}</dd>
              <dt>Current state</dt>
              <dd>{open.content.currentState || "—"}</dd>
              {(
                [
                  ["Completed work", open.content.completedWork],
                  ["Decisions", open.content.decisions],
                  ["Open questions", open.content.openQuestions],
                  ["Next actions", open.content.nextActions],
                  ["Risks", open.content.risks]
                ] as const
              ).map(([title, items]) =>
                items.length > 0 ? (
                  <div key={title}>
                    <dt>{title}</dt>
                    <dd>
                      <ul>
                        {items.map((item, index) => (
                          <li key={index}>{item}</li>
                        ))}
                      </ul>
                    </dd>
                  </div>
                ) : null
              )}
              <dt>Evidence</dt>
              <dd>{open.content.evidenceRefs.length} stored record(s) referenced</dd>
            </dl>
          ) : null}
          <div className="controls">
            {open.status === "draft" ? (
              <>
                <button type="button" disabled={busy} onClick={() => void run(async () => setOpen(await memory.review(open.id, "approve")))} data-testid="server-approve">
                  Approve
                </button>
                <button type="button" disabled={busy} onClick={() => void run(async () => setOpen(await memory.review(open.id, "reject")))} data-testid="server-reject">
                  Reject
                </button>
              </>
            ) : null}
            {open.status === "approved" ? (
              <button
                type="button"
                disabled={busy}
                data-testid="server-continue"
                title="Start a new session that carries this checkpoint forward"
                onClick={() =>
                  void run(async () => {
                    const next = newSessionId();
                    const carried = await memory.continueFrom(open.id, next, CONTINUATION_TOKEN_BUDGET);
                    onContinue(next, carried);
                    setOpen(null);
                  })
                }
              >
                Continue in a new session
              </button>
            ) : null}
            <button type="button" onClick={() => setOpen(null)}>
              Close
            </button>
          </div>
        </div>
      ) : null}
    </section>
  );
}
