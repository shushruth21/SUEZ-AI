import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { api, desktop, newId } from "./api";
import { CheckpointEditor } from "./components/CheckpointEditor";
import { HistoryFilter, HistoryList } from "./components/HistoryList";
import { Monitor } from "./components/Monitor";
import { ProjectPanel } from "./components/ProjectPanel";
import { SettingsPanel } from "./components/SettingsPanel";
import { createDraft, hasAutoCheckpointForSession, newestFor, newestForProject } from "./guardian/checkpoint";
import { MemoryProposal, diffMemory, importFromCheckpoint, newProjectMemory, proposeEdit, proposeMerge } from "./guardian/memory";
import { GuardEvent, GuardState, evaluate, guardStateForSession, initialGuardState } from "./guardian/threshold";
import { runSmoke } from "./smoke";
import {
  Checkpoint,
  CheckpointTrigger,
  DEFAULT_SETTINGS,
  GuardianSettings,
  ProjectList,
  ProjectMemory,
  UsageSnapshot
} from "./types";
import { GatewayUsageSource } from "./usage/gatewaySource";
import { MockUsageSource } from "./usage/mockSource";
import { UsageReading } from "./usage/source";

export type View = "monitor" | "editor" | "history" | "settings" | "project";

type Banner = { kind: "checkpoint" | "critical" | "error" | "info"; text: string; checkpointId?: string };

export type AppController = {
  setPercent: (percent: number) => void;
  view: () => View;
  reading: () => UsageReading;
  checkpointsCount: () => number;
  checkpoints: () => Checkpoint[];
  memory: () => ProjectMemory | null;
  activeProjectId: () => string;
};

const WINDOW_SIZES: Record<View, [number, number]> = {
  monitor: [360, 236],
  editor: [440, 680],
  history: [440, 520],
  settings: [420, 700],
  project: [460, 700]
};

const GATEWAY_MONITOR_SIZE: [number, number] = [380, 300];

// The mock simulation adds this share of the limit every tick.
const SIMULATION_TICK_MS = 1000;
const SIMULATION_STEP_PERCENT = 2;

function sessionLabelFor(date: Date) {
  return `Session ${date.toLocaleDateString()} ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

export default function App() {
  const mockRef = useRef(new MockUsageSource(DEFAULT_SETTINGS.limitTokens));
  const gatewayRef = useRef<GatewayUsageSource | null>(null);
  const guardRef = useRef<GuardState>(initialGuardState());
  const guardKeyRef = useRef("");
  const [reading, setReading] = useState<UsageReading>(() => mockRef.current.current());
  const [settings, setSettings] = useState<GuardianSettings>(DEFAULT_SETTINGS);
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([]);
  const [skipped, setSkipped] = useState<string[]>([]);
  const [storageLocation, setStorageLocation] = useState("");
  const [hasApiKey, setHasApiKey] = useState(false);
  const [ready, setReady] = useState(false);
  const [view, setView] = useState<View>("monitor");
  const [activeId, setActiveId] = useState<string | null>(null);
  const [banners, setBanners] = useState<Banner[]>([]);
  const [simulating, setSimulating] = useState(false);
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("project");
  const [projects, setProjects] = useState<ProjectList>({ projects: [], problems: [] });
  const [memory, setMemory] = useState<ProjectMemory | null>(null);
  const [memoryError, setMemoryError] = useState<string | null>(null);
  const [proposal, setProposal] = useState<MemoryProposal | null>(null);
  const [projectMessage, setProjectMessage] = useState("");
  const [mockSession, setMockSession] = useState(() => ({ id: newId(), label: sessionLabelFor(new Date()) }));

  const isGateway = settings.usageSource === "gateway";
  const session = useMemo(
    () =>
      isGateway
        ? { id: settings.gatewaySessionId, label: `Gateway session ${settings.gatewaySessionId.slice(0, 8)}` }
        : mockSession,
    [isGateway, settings.gatewaySessionId, mockSession]
  );

  // Refs give source callbacks the latest state without re-subscribing.
  const settingsRef = useRef(settings);
  settingsRef.current = settings;
  const checkpointsRef = useRef(checkpoints);
  checkpointsRef.current = checkpoints;
  const sessionRef = useRef(session);
  sessionRef.current = session;
  const viewRef = useRef(view);
  viewRef.current = view;
  const readingRef = useRef(reading);
  readingRef.current = reading;
  const memoryRef = useRef(memory);
  memoryRef.current = memory;

  const pushBanner = useCallback((banner: Banner) => {
    setBanners((previous) => [...previous.filter((existing) => existing.kind !== banner.kind), banner]);
  }, []);

  const storeCheckpoint = useCallback(async (checkpoint: Checkpoint) => {
    const saved = await api.saveCheckpoint(checkpoint);
    // Update the ref synchronously: a critical event in the same batch must see this draft.
    const rest = checkpointsRef.current.filter((existing) => existing.id !== saved.id);
    const next = [saved, ...rest].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
    checkpointsRef.current = next;
    setCheckpoints(next);
    return saved;
  }, []);

  const createCheckpoint = useCallback(
    async (trigger: CheckpointTrigger, usage: UsageSnapshot | null, thresholdPercent: number | null) => {
      const current = settingsRef.current;
      // With a selected project, drafts belong to it by stable id and are built
      // from its memory plus its newest checkpoint. Without one, legacy name mode.
      const projectMemory = current.activeProjectId && memoryRef.current?.projectId === current.activeProjectId ? memoryRef.current : null;
      const draft = createDraft({
        id: newId(),
        now: new Date(),
        projectName: projectMemory ? projectMemory.name : current.projectName,
        projectId: current.activeProjectId || undefined,
        sessionId: sessionRef.current.id,
        sessionLabel: sessionRef.current.label,
        trigger,
        thresholdPercent,
        usage,
        previous: current.activeProjectId
          ? newestForProject(checkpointsRef.current, current.activeProjectId)
          : newestFor(checkpointsRef.current, current.projectName),
        memory: projectMemory
      });
      try {
        const saved = await storeCheckpoint(draft);
        setActiveId(saved.id);
        setView("editor");
        return saved;
      } catch (error) {
        pushBanner({ kind: "error", text: `Could not save checkpoint draft: ${String(error)}` });
        return null;
      }
    },
    [pushBanner, storeCheckpoint]
  );

  const handleGuardEvents = useCallback(
    async (events: GuardEvent[], usage: UsageSnapshot) => {
      const label = usage.source === "mock" ? "simulated usage" : "measured by the SUEZ gateway";
      for (const event of events) {
        if (event.type === "checkpoint") {
          const saved = await createCheckpoint("auto_threshold", usage, event.threshold);
          if (saved) {
            pushBanner({
              kind: "checkpoint",
              text: `Draft checkpoint auto-saved at ${event.percent.toFixed(1)}% (${label}).`,
              checkpointId: saved.id
            });
            void desktop.requestAttention(false).catch(() => undefined);
          }
        } else {
          setSimulating(false);
          const sessionDraft = checkpointsRef.current.find((checkpoint) => checkpoint.sessionId === sessionRef.current.id);
          pushBanner({
            kind: "critical",
            text: `Critical: ${event.percent.toFixed(1)}% of the context window is used (${label}). Finish the checkpoint and start a new session.`,
            checkpointId: sessionDraft?.id
          });
          void desktop.requestAttention(true).catch(() => undefined);
        }
      }
    },
    [createCheckpoint, pushBanner]
  );

  // Every reading goes through here. Only a trustworthy current percentage
  // (guardPercent) reaches the threshold engine.
  const handleReading = useCallback(
    (next: UsageReading) => {
      setReading(next);
      if (next.guardPercent === null || next.display === null) return;
      const result = evaluate(guardRef.current, next.guardPercent, settingsRef.current);
      guardRef.current = result.state;
      if (result.events.length > 0) void handleGuardEvents(result.events, next.display);
    },
    [handleGuardEvents]
  );

  // Initial load: settings, history, storage path, key presence.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [stored, list, location, keyPresent, projectList] = await Promise.all([
          api.loadSettings(),
          api.listCheckpoints(),
          api.storageLocation(),
          api.gatewayHasApiKey().catch(() => false),
          api.listProjects().catch(() => ({ projects: [], problems: [] }) as ProjectList)
        ]);
        if (cancelled) return;
        let effective: GuardianSettings = { ...DEFAULT_SETTINGS, ...(stored ?? {}) };
        if (effective.usageSource === "gateway" && !effective.gatewaySessionId) {
          effective = { ...effective, gatewaySessionId: newId() };
          await api.saveSettings(effective);
        }
        setSettings(effective);
        settingsRef.current = effective;
        mockRef.current.setLimit(effective.limitTokens);
        setCheckpoints(list.checkpoints);
        checkpointsRef.current = list.checkpoints;
        setSkipped(list.skipped);
        setStorageLocation(location);
        setHasApiKey(keyPresent);
        setProjects(projectList);
        if (effective.activeProjectId) await loadMemory(effective.activeProjectId);
      } catch (error) {
        if (!cancelled) pushBanner({ kind: "error", text: `Could not load local data: ${String(error)}` });
      } finally {
        if (!cancelled) setReady(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [pushBanner]);

  // Active usage source. The guard only resets when the source kind or the
  // session changes, never on URL / interval edits, so no duplicate drafts.
  useEffect(() => {
    if (!ready) return;
    const sessionId = isGateway ? settings.gatewaySessionId : mockSession.id;
    const guardKey = `${settings.usageSource}:${sessionId}`;
    if (guardKeyRef.current !== guardKey) {
      // Seeded from the checkpoints on disk, so a restart that resumes the same
      // gateway session above the threshold does not create a second draft.
      guardRef.current = guardStateForSession(hasAutoCheckpointForSession(checkpointsRef.current, sessionId));
      guardKeyRef.current = guardKey;
    }

    if (!isGateway) {
      gatewayRef.current = null;
      setReading(mockRef.current.current());
      return mockRef.current.onReading(handleReading);
    }

    const source = new GatewayUsageSource((sessionId) => api.gatewayFetchUsage(settings.gatewayUrl, sessionId), {
      sessionId: settings.gatewaySessionId,
      intervalMs: settings.pollIntervalSeconds * 1000
    });
    gatewayRef.current = source;
    setReading(source.current());
    const unsubscribe = source.onReading(handleReading);
    source.start();
    return () => {
      source.stop();
      unsubscribe();
      if (gatewayRef.current === source) gatewayRef.current = null;
    };
  }, [ready, isGateway, settings.usageSource, settings.gatewayUrl, settings.gatewaySessionId, settings.pollIntervalSeconds, mockSession.id, handleReading]);

  useEffect(() => {
    if (!simulating || isGateway) return;
    const timer = window.setInterval(() => {
      const source = mockRef.current;
      source.step((source.snapshot().limitTokens * SIMULATION_STEP_PERCENT) / 100);
      if (source.snapshot().percent >= 100) setSimulating(false);
    }, SIMULATION_TICK_MS);
    return () => window.clearInterval(timer);
  }, [simulating, isGateway]);

  useEffect(() => {
    const [width, height] = view === "monitor" && isGateway ? GATEWAY_MONITOR_SIZE : WINDOW_SIZES[view];
    void desktop.resize(width, height).catch(() => undefined);
  }, [view, isGateway]);

  // Self-check mode only runs when the native side was started with the smoke env vars.
  useEffect(() => {
    if (!ready) return;
    let started = false;
    void api.smokePhase().then((phase) => {
      if (!phase || started) return;
      started = true;
      const controller: AppController = {
        setPercent: (percent) => mockRef.current.setPercent(percent),
        view: () => viewRef.current,
        reading: () => readingRef.current,
        checkpointsCount: () => checkpointsRef.current.length,
        checkpoints: () => checkpointsRef.current,
        memory: () => memoryRef.current,
        activeProjectId: () => settingsRef.current.activeProjectId
      };
      void runSmoke(phase, controller);
    });
    return () => {
      started = true;
    };
  }, [ready]);

  const loadRevisions = useCallback(
    () => (settings.activeProjectId ? api.listMemoryRevisions(settings.activeProjectId) : Promise.resolve([])),
    [settings.activeProjectId]
  );

  const active = useMemo(() => checkpoints.find((checkpoint) => checkpoint.id === activeId) ?? null, [checkpoints, activeId]);

  async function persistSettings(next: GuardianSettings) {
    await api.saveSettings(next);
    setSettings(next);
    settingsRef.current = next;
    mockRef.current.setLimit(next.limitTokens);
  }

  async function startNewSession() {
    setSimulating(false);
    setBanners([]);
    if (isGateway) {
      try {
        await persistSettings({ ...settingsRef.current, gatewaySessionId: newId() });
      } catch (error) {
        pushBanner({ kind: "error", text: `Could not start a new session: ${String(error)}` });
      }
      return;
    }
    setMockSession({ id: newId(), label: sessionLabelFor(new Date()) });
    mockRef.current.reset();
  }

  async function saveSettings(next: GuardianSettings) {
    const previous = settingsRef.current;
    const withSession =
      next.usageSource === "gateway" && !next.gatewaySessionId ? { ...next, gatewaySessionId: newId() } : next;
    await persistSettings(withSession);
    if (previous.usageSource !== withSession.usageSource) {
      setSimulating(false);
      setBanners([]);
      pushBanner({
        kind: "info",
        text:
          withSession.usageSource === "gateway"
            ? "Usage source switched to the SUEZ gateway. Simulated data is no longer used."
            : "Usage source switched to MOCK. Figures are simulated, not measured."
      });
    }
  }

  async function setApiKey(apiKey: string) {
    await api.gatewaySetApiKey(apiKey);
    setHasApiKey(true);
    void gatewayRef.current?.poll();
  }

  async function clearApiKey() {
    await api.gatewayClearApiKey();
    setHasApiKey(false);
    void gatewayRef.current?.poll();
  }

  async function copySessionHeader() {
    try {
      await desktop.copy(`X-SUEZ-Session-Id: ${settingsRef.current.gatewaySessionId}`);
      pushBanner({ kind: "info", text: "Copied the session header. Send it with each chat request to the gateway." });
    } catch (error) {
      pushBanner({ kind: "error", text: `Copy failed: ${String(error)}` });
    }
  }

  async function deleteCheckpoint(id: string) {
    try {
      await api.deleteCheckpoint(id);
      setCheckpoints((previous) => previous.filter((checkpoint) => checkpoint.id !== id));
      setBanners((previous) => previous.filter((banner) => banner.checkpointId !== id));
      setActiveId(null);
      setView("history");
    } catch (error) {
      pushBanner({ kind: "error", text: `Could not delete checkpoint: ${String(error)}` });
    }
  }

  // ---- Project memory -----------------------------------------------------
  // Memory changes only through a proposal the user approves in ProjectPanel.

  async function loadMemory(projectId: string) {
    try {
      const loaded = await api.getProjectMemory(projectId);
      setMemory(loaded);
      memoryRef.current = loaded;
      setMemoryError(loaded ? null : `project ${projectId} was not found`);
    } catch (error) {
      setMemory(null);
      memoryRef.current = null;
      setMemoryError(String(error));
    }
  }

  async function refreshProjects() {
    setProjects(await api.listProjects());
  }

  async function selectProject(projectId: string) {
    await persistSettings({ ...settingsRef.current, activeProjectId: projectId });
    setProposal(null);
    setProjectMessage("");
    setHistoryFilter("project");
    if (projectId) await loadMemory(projectId);
    else {
      setMemory(null);
      memoryRef.current = null;
      setMemoryError(null);
    }
  }

  async function createProject(name: string) {
    try {
      const created = await api.createProject(newProjectMemory(newId(), name, new Date()));
      await refreshProjects();
      await selectProject(created.projectId);
      setProjectMessage(`Created project “${created.name}”.`);
    } catch (error) {
      setProjectMessage(`Could not create the project: ${String(error)}`);
    }
  }

  function startImport(checkpoint: Checkpoint) {
    const next = importFromCheckpoint(checkpoint, newId(), checkpoint.projectName, new Date());
    const empty = { ...newProjectMemory(next.projectId, next.name, new Date(next.createdAt)), sources: [] };
    setProposal({ kind: "import", baseRevision: null, next, changes: diffMemory(empty, next), sourceCheckpointId: checkpoint.id });
    setProjectMessage("");
    setView("project");
  }

  function startMerge(checkpoint: Checkpoint) {
    const current = memoryRef.current;
    if (!current || current.projectId !== checkpoint.projectId) return;
    setProposal(proposeMerge(current, checkpoint, new Date()));
    setProjectMessage("");
    setView("project");
  }

  function startEdit(edited: ProjectMemory) {
    const current = memoryRef.current;
    if (!current) return;
    setProposal(proposeEdit(current, edited, new Date()));
  }

  async function approveProposal() {
    const pending = proposal;
    if (!pending) return;
    try {
      if (pending.kind === "import") {
        const created = await api.createProject(pending.next);
        await refreshProjects();
        await selectProject(created.projectId);
        setProjectMessage(`Created “${created.name}” from checkpoint ${pending.sourceCheckpointId}. The checkpoint itself was not changed.`);
      } else {
        const saved = await api.saveProjectMemory(pending.next, pending.baseRevision ?? 0);
        setMemory(saved);
        memoryRef.current = saved;
        setProposal(null);
        await refreshProjects();
        setProjectMessage(`Saved project memory revision ${saved.revision}.`);
      }
    } catch (error) {
      setProposal(null);
      setProjectMessage(`Not saved: ${String(error)}`);
      const projectId = settingsRef.current.activeProjectId;
      if (projectId) await loadMemory(projectId);
    }
  }

  async function restoreRevision(revision: number) {
    const projectId = settingsRef.current.activeProjectId;
    if (!projectId) return;
    try {
      const expected = memoryError ? null : memoryRef.current?.revision ?? null;
      const restored = await api.restoreMemoryRevision(projectId, revision, expected, new Date().toISOString());
      setMemory(restored);
      memoryRef.current = restored;
      setMemoryError(null);
      await refreshProjects();
      setProjectMessage(`Restored revision ${revision} as revision ${restored.revision}.`);
    } catch (error) {
      setProjectMessage(`Restore failed: ${String(error)}`);
    }
  }

  // Explicit assignment of a legacy checkpoint to the selected project. The
  // save upgrades it to schema 2; Rust backs up the original file first.
  async function assignCheckpoint(checkpoint: Checkpoint): Promise<Checkpoint | null> {
    const projectId = settingsRef.current.activeProjectId;
    if (!projectId) return null;
    try {
      const saved = await storeCheckpoint({ ...checkpoint, projectId, updatedAt: new Date().toISOString() });
      pushBanner({ kind: "info", text: "Checkpoint assigned to the project. A backup of the original file was kept." });
      return saved;
    } catch (error) {
      pushBanner({ kind: "error", text: `Could not assign the checkpoint: ${String(error)}` });
      return null;
    }
  }

  // Prompts are checked for the stored gateway API key before they are copied.
  async function copyPrompt(text: string) {
    await api.checkTextForSecret(text);
    await desktop.copy(text);
  }

  function openCheckpoint(id: string) {
    setActiveId(id);
    setView("editor");
  }

  const gatewayState = reading.gateway;
  const dataState = !isGateway
    ? null
    : reading.guardPercent !== null
      ? null
      : gatewayState?.stale
        ? "STALE"
        : "NO DATA";
  const level = reading.guardPercent === null ? "unknown" : reading.guardPercent >= settings.criticalPercent ? "critical" : reading.guardPercent >= settings.checkpointPercent ? "checkpoint" : "normal";

  return (
    <div className={`app level-${level}`}>
      <header className="titlebar" data-tauri-drag-region>
        <span className="title" data-tauri-drag-region>
          Context Guardian
        </span>
        {isGateway ? (
          <span className="source-badge" data-testid="source-badge" title="Usage measured by the SUEZ gateway">
            GATEWAY
          </span>
        ) : (
          <span className="mock-badge" data-testid="mock-badge" title="All usage figures are simulated. No provider is connected.">
            MOCK DATA
          </span>
        )}
        {isGateway && reading.display?.measurementSource === "gateway_estimate" ? (
          <span className="estimate-badge" data-testid="estimate-badge" title="Token counts are estimated by the gateway (stub model), not reported by a provider">
            ESTIMATE
          </span>
        ) : null}
        {dataState ? (
          <span className="data-state-badge" data-testid="data-state">
            {dataState}
          </span>
        ) : null}
        <span className="spacer" data-tauri-drag-region />
        <button type="button" className="icon" title="Monitor" onClick={() => setView("monitor")} data-testid="nav-monitor">
          ◔
        </button>
        <button type="button" className="icon" title="Project memory" onClick={() => setView("project")} data-testid="nav-project">
          ◆
        </button>
        <button type="button" className="icon" title="Checkpoint history" onClick={() => setView("history")} data-testid="nav-history">
          ☰
        </button>
        <button type="button" className="icon" title="Settings" onClick={() => setView("settings")} data-testid="nav-settings">
          ⚙
        </button>
        <button type="button" className="icon" title="Minimize" onClick={() => void desktop.minimize()}>
          –
        </button>
        <button type="button" className="icon" title="Close" onClick={() => void desktop.close()}>
          ×
        </button>
      </header>

      {banners.map((banner) => (
        <div key={banner.kind} className={`banner banner-${banner.kind}`} data-testid={`banner-${banner.kind}`}>
          <span>{banner.text}</span>
          {banner.checkpointId ? (
            <button type="button" onClick={() => openCheckpoint(banner.checkpointId!)} data-testid={`banner-open-${banner.kind}`}>
              Open
            </button>
          ) : null}
          <button type="button" className="icon" title="Dismiss" onClick={() => setBanners((previous) => previous.filter((b) => b !== banner))}>
            ×
          </button>
        </div>
      ))}

      <main className="content">
        {view === "monitor" || (view === "editor" && !active) ? (
          <Monitor
            reading={reading}
            settings={settings}
            sessionLabel={session.label}
            sessionId={session.id}
            simulating={simulating}
            onSetPercent={(percent) => mockRef.current.setPercent(percent)}
            onStep={(delta) => mockRef.current.setPercent(mockRef.current.snapshot().percent + delta)}
            onToggleSimulation={() => setSimulating((value) => !value)}
            onNewSession={() => void startNewSession()}
            onManualCheckpoint={() => {
              const current = readingRef.current;
              void createCheckpoint("manual", current.guardPercent !== null ? current.display : null, null);
            }}
            onRefresh={() => void gatewayRef.current?.poll()}
            onCopySessionHeader={() => void copySessionHeader()}
          />
        ) : null}
        {view === "editor" && active ? (
          <CheckpointEditor
            checkpoint={active}
            memory={memory}
            activeProject={settings.activeProjectId && memory ? { id: memory.projectId, name: memory.name } : null}
            onSave={storeCheckpoint}
            onDelete={deleteCheckpoint}
            onCopy={copyPrompt}
            onClose={() => setView("monitor")}
            onAssign={assignCheckpoint}
            onImport={startImport}
            onProposeMerge={startMerge}
          />
        ) : null}
        {view === "history" ? (
          <HistoryList
            checkpoints={checkpoints}
            skipped={skipped}
            projectName={settings.projectName}
            activeProject={
              settings.activeProjectId && memory
                ? { id: memory.projectId, name: memory.name, sourceIds: memory.sources.map((s) => s.checkpointId) }
                : null
            }
            filter={historyFilter}
            onFilter={setHistoryFilter}
            onOpen={openCheckpoint}
            onBack={() => setView("monitor")}
          />
        ) : null}
        {view === "project" ? (
          <ProjectPanel
            projects={projects}
            activeProjectId={settings.activeProjectId}
            memory={memory}
            memoryError={memoryError}
            proposal={proposal}
            message={projectMessage}
            onSelect={selectProject}
            onCreate={createProject}
            onProposeEdit={startEdit}
            onApprove={approveProposal}
            onCancelProposal={() => setProposal(null)}
            loadRevisions={loadRevisions}
            onRestore={restoreRevision}
            onBack={() => setView("monitor")}
          />
        ) : null}
        {view === "settings" ? (
          <SettingsPanel
            key={JSON.stringify(settings)}
            settings={settings}
            storageLocation={storageLocation}
            hasApiKey={hasApiKey}
            onSave={saveSettings}
            onSetApiKey={setApiKey}
            onClearApiKey={clearApiKey}
            onTestConnection={(baseUrl) => api.gatewayTestConnection(baseUrl)}
            onBack={() => setView("monitor")}
          />
        ) : null}
      </main>
    </div>
  );
}
