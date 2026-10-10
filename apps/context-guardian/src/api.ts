import { invoke } from "@tauri-apps/api/core";
import { LogicalSize, UserAttentionType, getCurrentWindow } from "@tauri-apps/api/window";
import { writeText } from "@tauri-apps/plugin-clipboard-manager";

import { Checkpoint, GuardianSettings, ProjectList, ProjectMemory, RevisionSummary } from "./types";
import { GatewayContextUsage, GatewayResult } from "./usage/gatewayReading";

export type ListResult = { checkpoints: Checkpoint[]; skipped: string[] };

export type GatewayIdentity = { actor: string; tenantSlug: string; authMethod: string };

export const api = {
  listCheckpoints: () => invoke<ListResult>("list_checkpoints"),
  getCheckpoint: (id: string) => invoke<Checkpoint | null>("get_checkpoint", { id }),
  saveCheckpoint: (checkpoint: Checkpoint) => invoke<Checkpoint>("save_checkpoint", { checkpoint }),
  deleteCheckpoint: (id: string) => invoke<boolean>("delete_checkpoint", { id }),
  loadSettings: () => invoke<GuardianSettings | null>("load_settings"),
  saveSettings: (settings: GuardianSettings) => invoke<void>("save_settings", { settings }),
  storageLocation: () => invoke<string>("storage_location"),
  checkTextForSecret: (text: string) => invoke<void>("check_text_for_secret", { text }),
  listProjects: () => invoke<ProjectList>("list_projects"),
  getProjectMemory: (projectId: string) => invoke<ProjectMemory | null>("get_project_memory", { projectId }),
  createProject: (memory: ProjectMemory) => invoke<ProjectMemory>("create_project", { memory }),
  // Only called after the user approved the preview; Rust refuses stale revisions.
  saveProjectMemory: (memory: ProjectMemory, expectedRevision: number) =>
    invoke<ProjectMemory>("save_project_memory", { memory, expectedRevision }),
  listMemoryRevisions: (projectId: string) => invoke<RevisionSummary[]>("list_memory_revisions", { projectId }),
  getMemoryRevision: (projectId: string, revision: number) =>
    invoke<ProjectMemory>("get_memory_revision", { projectId, revision }),
  restoreMemoryRevision: (projectId: string, revision: number, expectedRevision: number | null, updatedAt: string) =>
    invoke<ProjectMemory>("restore_memory_revision", { projectId, revision, expectedRevision, updatedAt }),
  // The API key goes to Rust (macOS Keychain) and is never read back.
  gatewayHasApiKey: () => invoke<boolean>("gateway_has_api_key"),
  gatewaySetApiKey: (apiKey: string) => invoke<void>("gateway_set_api_key", { apiKey }),
  gatewayClearApiKey: () => invoke<void>("gateway_clear_api_key"),
  gatewayFetchUsage: (baseUrl: string, sessionId: string) =>
    invoke<GatewayResult<GatewayContextUsage>>("gateway_fetch_usage", { baseUrl, sessionId }),
  gatewayTestConnection: (baseUrl: string) =>
    invoke<GatewayResult<GatewayIdentity>>("gateway_test_connection", { baseUrl }),
  smokePhase: () => invoke<string | null>("smoke_mode"),
  smokeSignal: (name: string) => invoke<void>("smoke_signal", { name }),
  smokeReport: (report: unknown) => invoke<void>("smoke_report", { report })
};

export const desktop = {
  copy: (text: string) => writeText(text),
  resize: (width: number, height: number) => getCurrentWindow().setSize(new LogicalSize(width, height)),
  close: () => getCurrentWindow().close(),
  minimize: () => getCurrentWindow().minimize(),
  requestAttention: (critical: boolean) =>
    getCurrentWindow().requestUserAttention(
      critical ? UserAttentionType.Critical : UserAttentionType.Informational
    )
};

export function newId(): string {
  return crypto.randomUUID();
}
