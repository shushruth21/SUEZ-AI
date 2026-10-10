//! Persistent project memory: one versioned record per project, independent of
//! any single checkpoint. Stored by the same `Store` as checkpoints:
//!
//! ```text
//! <data dir>/projects/<projectId>/memory.json            current memory
//! <data dir>/projects/<projectId>/revisions/<n>.json     previous revisions (latest 50 kept)
//! <data dir>/projects/<projectId>/memory.corrupt-<ms>.json   quarantined unreadable file
//! ```
//!
//! Every save names the revision it was based on; a stale proposal is refused.
//! Before the current memory is replaced it is copied to `revisions/` and the
//! copy is verified. Items are never removed by a normal save (they can only be
//! marked superseded / resolved; plain text lines are edited only through the
//! user-reviewed preview); only an explicit restore of an older
//! revision may drop items, and that restore is itself a new, reversible revision.

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fs;
use std::path::PathBuf;

use crate::storage::{
    check_verified, ensure_unique_ids, unix_millis, validate_id, write_atomic, DecisionRecord, Provenance,
    Store, TestResult, WorkItem,
};

pub const MEMORY_SCHEMA_VERSION: u32 = 1;
pub const MEMORY_REVISIONS_KEPT: usize = 50;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ApprovalStatus {
    Awaiting,
    Granted,
    Declined,
}

/// An operation that needs the user's approval. Recording or changing one is
/// bookkeeping only; nothing is ever executed because of it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Approval {
    pub id: String,
    pub item: String,
    pub status: ApprovalStatus,
    #[serde(default)]
    pub note: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct NextAction {
    pub text: String,
    pub set_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_checkpoint_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MemorySource {
    pub checkpoint_id: String,
    pub applied_at: String,
    /// "import" (created from a checkpoint) or "merge" (update from a checkpoint).
    pub kind: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectMemory {
    pub schema_version: u32,
    pub project_id: String,
    pub name: String,
    pub created_at: String,
    pub updated_at: String,
    pub revision: u64,
    #[serde(default)]
    pub objective: String,
    #[serde(default)]
    pub requirements: Vec<String>,
    #[serde(default)]
    pub constraints: Vec<String>,
    #[serde(default)]
    pub architecture: String,
    /// Plain "key: value" facts about the environment. Never secrets.
    #[serde(default)]
    pub environment: Vec<String>,
    #[serde(default)]
    pub decisions: Vec<DecisionRecord>,
    /// Milestones and completed work (verified / reported) and outstanding
    /// work (pending / failed), distinguished by status.
    #[serde(default)]
    pub work_items: Vec<WorkItem>,
    #[serde(default)]
    pub test_results: Vec<TestResult>,
    #[serde(default)]
    pub current_status: String,
    #[serde(default)]
    pub known_issues: Vec<String>,
    #[serde(default)]
    pub required_approvals: Vec<Approval>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub next_action: Option<NextAction>,
    #[serde(default)]
    pub notes: String,
    #[serde(default)]
    pub sources: Vec<MemorySource>,
}

impl ProjectMemory {
    pub fn validate(&self) -> Result<(), String> {
        if self.schema_version != MEMORY_SCHEMA_VERSION {
            return Err(format!("unsupported project memory schema version {}", self.schema_version));
        }
        validate_id(&self.project_id).map_err(|_| format!("invalid project id {:?}", self.project_id))?;
        if self.name.trim().is_empty() {
            return Err("project name is required".into());
        }
        if self.revision == 0 {
            return Err("memory revision starts at 1".into());
        }
        ensure_unique_ids("decisions", self.decisions.iter().map(|d| d.id.as_str()))?;
        ensure_unique_ids("work items", self.work_items.iter().map(|w| w.id.as_str()))?;
        ensure_unique_ids("test results", self.test_results.iter().map(|t| t.id.as_str()))?;
        ensure_unique_ids("approvals", self.required_approvals.iter().map(|a| a.id.as_str()))?;
        for item in &self.work_items {
            if item.text.trim().is_empty() {
                return Err("work items must have text".into());
            }
            // Memory spans sessions: verified needs verifying evidence from the
            // session it was recorded in (any session), never a prior report.
            check_verified(item, None)?;
        }
        for test in &self.test_results {
            if test.provenance == Provenance::Carried && test.carried_from.as_deref().map_or(true, |c| c.trim().is_empty()) {
                return Err(format!("carried test result {:?} must say which checkpoint it came from", test.suite));
            }
        }
        if let Some(next) = &self.next_action {
            if next.text.trim().is_empty() {
                return Err("next action must have text".into());
            }
        }
        Ok(())
    }
}

/// A normal save may change items with ids (decisions, work items, test
/// results, approvals, sources) but never drop them. Plain text lists
/// (requirements, constraints, environment, known issues) can be edited by the
/// user; the app shows every removed line in the preview before saving.
fn ensure_nothing_removed(current: &ProjectMemory, next: &ProjectMemory) -> Result<(), String> {
    fn missing<'a>(label: &str, before: impl Iterator<Item = &'a str>, after: HashSet<&str>) -> Result<(), String> {
        for id in before {
            if !after.contains(id) {
                return Err(format!(
                    "the update would remove {label} {id:?}; mark it superseded or resolved instead"
                ));
            }
        }
        Ok(())
    }
    missing("decision", current.decisions.iter().map(|d| d.id.as_str()), next.decisions.iter().map(|d| d.id.as_str()).collect())?;
    missing("work item", current.work_items.iter().map(|w| w.id.as_str()), next.work_items.iter().map(|w| w.id.as_str()).collect())?;
    missing("test result", current.test_results.iter().map(|t| t.id.as_str()), next.test_results.iter().map(|t| t.id.as_str()).collect())?;
    missing("approval", current.required_approvals.iter().map(|a| a.id.as_str()), next.required_approvals.iter().map(|a| a.id.as_str()).collect())?;
    let sources: HashSet<&str> = next.sources.iter().map(|s| s.checkpoint_id.as_str()).collect();
    missing("source", current.sources.iter().map(|s| s.checkpoint_id.as_str()), sources)?;
    Ok(())
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectSummary {
    pub project_id: String,
    pub name: String,
    pub revision: u64,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectProblem {
    pub project_id: String,
    pub error: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectList {
    pub projects: Vec<ProjectSummary>,
    /// Projects whose memory could not be read; their files are left untouched.
    pub problems: Vec<ProjectProblem>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RevisionSummary {
    pub revision: u64,
    pub updated_at: String,
}

fn parse_memory(bytes: &[u8]) -> Result<ProjectMemory, String> {
    let memory: ProjectMemory = serde_json::from_slice(bytes).map_err(|e| e.to_string())?;
    if memory.schema_version != MEMORY_SCHEMA_VERSION {
        return Err(format!("unsupported project memory schema version {}", memory.schema_version));
    }
    Ok(memory)
}

enum Current {
    Missing,
    Unreadable(String),
    Ok(ProjectMemory, Vec<u8>),
}

impl Store {
    fn project_dir(&self, project_id: &str) -> Result<PathBuf, String> {
        validate_id(project_id).map_err(|_| format!("invalid project id {project_id:?}"))?;
        Ok(self.root().join("projects").join(project_id))
    }

    fn memory_path(&self, project_id: &str) -> Result<PathBuf, String> {
        Ok(self.project_dir(project_id)?.join("memory.json"))
    }

    fn revision_path(&self, project_id: &str, revision: u64) -> Result<PathBuf, String> {
        Ok(self.project_dir(project_id)?.join("revisions").join(format!("{revision:010}.json")))
    }

    fn read_current(&self, project_id: &str) -> Result<Current, String> {
        match fs::read(self.memory_path(project_id)?) {
            Ok(bytes) => match parse_memory(&bytes) {
                Ok(memory) if memory.project_id == project_id => Ok(Current::Ok(memory, bytes)),
                Ok(memory) => Ok(Current::Unreadable(format!(
                    "memory file belongs to project {:?}",
                    memory.project_id
                ))),
                Err(e) => Ok(Current::Unreadable(e)),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Current::Missing),
            Err(e) => Err(e.to_string()),
        }
    }

    /// Writes `memory` as the current file and verifies it reads back identically.
    fn write_memory_verified(&self, memory: &ProjectMemory) -> Result<ProjectMemory, String> {
        let path = self.memory_path(&memory.project_id)?;
        let payload = serde_json::to_vec_pretty(memory).map_err(|e| e.to_string())?;
        write_atomic(&path, &payload)?;
        match self.read_current(&memory.project_id)? {
            Current::Ok(read_back, _) if &read_back == memory => Ok(read_back),
            _ => Err(format!("project memory {} did not read back correctly after saving", memory.project_id)),
        }
    }

    /// Copies the current memory bytes to `revisions/` and verifies the copy.
    fn archive_revision(&self, memory: &ProjectMemory, bytes: &[u8]) -> Result<(), String> {
        let path = self.revision_path(&memory.project_id, memory.revision)?;
        write_atomic(&path, bytes)?;
        if fs::read(&path).map_err(|e| e.to_string())? != bytes {
            return Err("could not verify the revision backup; memory was not changed".into());
        }
        Ok(())
    }

    fn revision_numbers(&self, project_id: &str) -> Result<Vec<u64>, String> {
        let dir = self.project_dir(project_id)?.join("revisions");
        let mut numbers: Vec<u64> = match fs::read_dir(&dir) {
            Ok(entries) => entries
                .flatten()
                .filter_map(|e| {
                    let name = e.file_name().to_string_lossy().to_string();
                    name.strip_suffix(".json")?.parse::<u64>().ok()
                })
                .collect(),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => vec![],
            Err(e) => return Err(e.to_string()),
        };
        numbers.sort_unstable();
        Ok(numbers)
    }

    /// Keeps the newest `MEMORY_REVISIONS_KEPT` revision files. The current
    /// memory.json is never touched here.
    fn prune_revisions(&self, project_id: &str) -> Result<(), String> {
        let numbers = self.revision_numbers(project_id)?;
        if numbers.len() <= MEMORY_REVISIONS_KEPT {
            return Ok(());
        }
        for revision in &numbers[..numbers.len() - MEMORY_REVISIONS_KEPT] {
            fs::remove_file(self.revision_path(project_id, *revision)?).map_err(|e| e.to_string())?;
        }
        Ok(())
    }

    pub fn list_projects(&self) -> Result<ProjectList, String> {
        let mut projects = Vec::new();
        let mut problems = Vec::new();
        let entries = match fs::read_dir(self.root().join("projects")) {
            Ok(entries) => entries,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(ProjectList { projects, problems }),
            Err(e) => return Err(e.to_string()),
        };
        for entry in entries.flatten() {
            let project_id = entry.file_name().to_string_lossy().to_string();
            if !entry.path().is_dir() || validate_id(&project_id).is_err() {
                continue;
            }
            match self.read_current(&project_id)? {
                Current::Ok(memory, _) => projects.push(ProjectSummary {
                    project_id,
                    name: memory.name,
                    revision: memory.revision,
                    updated_at: memory.updated_at,
                }),
                Current::Missing => problems.push(ProjectProblem { project_id, error: "memory.json is missing".into() }),
                Current::Unreadable(error) => problems.push(ProjectProblem { project_id, error }),
            }
        }
        projects.sort_by(|a, b| a.name.to_lowercase().cmp(&b.name.to_lowercase()).then_with(|| a.project_id.cmp(&b.project_id)));
        problems.sort_by(|a, b| a.project_id.cmp(&b.project_id));
        Ok(ProjectList { projects, problems })
    }

    /// `Ok(None)` if the project does not exist; an error if its memory is unreadable.
    pub fn get_memory(&self, project_id: &str) -> Result<Option<ProjectMemory>, String> {
        match self.read_current(project_id)? {
            Current::Ok(memory, _) => Ok(Some(memory)),
            Current::Missing => Ok(None),
            Current::Unreadable(e) => Err(format!("project memory {project_id} is unreadable: {e}")),
        }
    }

    pub fn create_project(&self, memory: &ProjectMemory) -> Result<ProjectMemory, String> {
        memory.validate()?;
        if memory.revision != 1 {
            return Err("a new project starts at revision 1".into());
        }
        if self.project_dir(&memory.project_id)?.exists() {
            return Err(format!("project {} already exists", memory.project_id));
        }
        self.write_memory_verified(memory)
    }

    /// Saves an approved update. `expected_revision` is the revision the
    /// proposal was based on; if memory has moved on, nothing is written.
    pub fn save_memory(&self, memory: &ProjectMemory, expected_revision: u64) -> Result<ProjectMemory, String> {
        let (current, bytes) = match self.read_current(&memory.project_id)? {
            Current::Ok(current, bytes) => (current, bytes),
            Current::Missing => return Err(format!("project {} does not exist", memory.project_id)),
            Current::Unreadable(e) => {
                return Err(format!(
                    "project memory is unreadable ({e}); restore a revision before saving"
                ))
            }
        };
        if current.revision != expected_revision {
            return Err(format!(
                "conflict: memory is at revision {} but this update was based on revision {expected_revision}; reload and review again",
                current.revision
            ));
        }
        let mut next = memory.clone();
        next.revision = current.revision + 1;
        next.created_at = current.created_at.clone();
        next.validate()?;
        ensure_nothing_removed(&current, &next)?;
        self.archive_revision(&current, &bytes)?;
        let saved = self.write_memory_verified(&next)?;
        self.prune_revisions(&next.project_id)?;
        Ok(saved)
    }

    /// Previous revisions, newest first. Unreadable revision files are skipped.
    pub fn list_memory_revisions(&self, project_id: &str) -> Result<Vec<RevisionSummary>, String> {
        let mut out = Vec::new();
        for revision in self.revision_numbers(project_id)?.into_iter().rev() {
            if let Ok(memory) = self.get_memory_revision(project_id, revision) {
                out.push(RevisionSummary { revision, updated_at: memory.updated_at });
            }
        }
        Ok(out)
    }

    pub fn get_memory_revision(&self, project_id: &str, revision: u64) -> Result<ProjectMemory, String> {
        let bytes = fs::read(self.revision_path(project_id, revision)?)
            .map_err(|e| format!("revision {revision} is not available: {e}"))?;
        let memory = parse_memory(&bytes).map_err(|e| format!("revision {revision} is unreadable: {e}"))?;
        if memory.project_id != project_id {
            return Err(format!("revision {revision} belongs to another project"));
        }
        Ok(memory)
    }

    /// Restores an older revision as a new revision. With readable memory,
    /// `expected_revision` must match the current one and the current memory
    /// is archived first. With unreadable memory, pass `None`: the unreadable
    /// file is renamed to `memory.corrupt-<ms>.json` (never deleted).
    pub fn restore_memory_revision(
        &self,
        project_id: &str,
        revision: u64,
        expected_revision: Option<u64>,
        updated_at: &str,
    ) -> Result<ProjectMemory, String> {
        let mut restored = self.get_memory_revision(project_id, revision)?;
        restored.updated_at = updated_at.to_string();
        let latest_archived = self.revision_numbers(project_id)?.last().copied().unwrap_or(0);
        match self.read_current(project_id)? {
            Current::Ok(current, bytes) => {
                if expected_revision != Some(current.revision) {
                    return Err(format!(
                        "conflict: memory is at revision {}; reload before restoring",
                        current.revision
                    ));
                }
                restored.revision = current.revision.max(latest_archived) + 1;
                restored.validate()?;
                self.archive_revision(&current, &bytes)?;
            }
            Current::Unreadable(_) | Current::Missing => {
                if expected_revision.is_some() {
                    return Err("memory is unreadable or missing; restore with recovery instead".into());
                }
                let path = self.memory_path(project_id)?;
                if path.exists() {
                    let quarantine = self.project_dir(project_id)?.join(format!("memory.corrupt-{}.json", unix_millis()));
                    fs::rename(&path, &quarantine).map_err(|e| e.to_string())?;
                }
                restored.revision = latest_archived + 1;
                restored.validate()?;
            }
        }
        let saved = self.write_memory_verified(&restored)?;
        self.prune_revisions(project_id)?;
        Ok(saved)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::storage::{DecisionStatus, Evidence, EvidenceKind, EvidenceStatus};

    fn memory(id: &str) -> ProjectMemory {
        ProjectMemory {
            schema_version: MEMORY_SCHEMA_VERSION,
            project_id: id.to_string(),
            name: "SUEZ Context Guardian".to_string(),
            created_at: "2026-10-10T00:00:00.000Z".to_string(),
            updated_at: "2026-10-10T00:00:00.000Z".to_string(),
            revision: 1,
            objective: "Preserve project state".to_string(),
            requirements: vec![],
            constraints: vec!["No paid provider calls".to_string()],
            architecture: String::new(),
            environment: vec![],
            decisions: vec![DecisionRecord {
                id: "d1".into(),
                text: "Use JSON files".into(),
                rationale: String::new(),
                decided_at: "2026-10-10T00:00:00.000Z".into(),
                status: DecisionStatus::Active,
            }],
            work_items: vec![WorkItem {
                id: "w1".into(),
                text: "Gateway integration".into(),
                status: EvidenceStatus::Reported,
                evidence: vec![],
                carried_from: None,
            }],
            test_results: vec![],
            current_status: String::new(),
            known_issues: vec![],
            required_approvals: vec![Approval {
                id: "a1".into(),
                item: "Apply migration 0006".into(),
                status: ApprovalStatus::Awaiting,
                note: String::new(),
                updated_at: "2026-10-10T00:00:00.000Z".into(),
            }],
            next_action: None,
            notes: String::new(),
            sources: vec![MemorySource {
                checkpoint_id: "cp-1".into(),
                applied_at: "2026-10-10T00:00:00.000Z".into(),
                kind: "import".into(),
            }],
        }
    }

    #[test]
    fn user_reviewed_text_list_edits_are_allowed() {
        let (_dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        let mut update = memory("p1");
        update.constraints.clear();
        assert_eq!(store.save_memory(&update, 1).unwrap().constraints, Vec::<String>::new());
        assert_eq!(store.get_memory_revision("p1", 1).unwrap().constraints, vec!["No paid provider calls"]);
    }

    fn setup() -> (tempfile::TempDir, Store) {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        (dir, store)
    }

    #[test]
    fn create_get_list_round_trip() {
        let (_dir, store) = setup();
        assert!(store.list_projects().unwrap().projects.is_empty());
        let created = store.create_project(&memory("p1")).unwrap();
        assert_eq!(store.get_memory("p1").unwrap(), Some(created));
        assert_eq!(store.get_memory("nope").unwrap(), None);
        let listed = store.list_projects().unwrap();
        assert_eq!(listed.projects.len(), 1);
        assert_eq!(listed.projects[0].name, "SUEZ Context Guardian");
        assert!(store.create_project(&memory("p1")).unwrap_err().contains("already exists"));
    }

    #[test]
    fn projects_with_the_same_name_stay_separate() {
        let (_dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        store.create_project(&memory("p2")).unwrap();
        let mut changed = memory("p2");
        changed.objective = "Different".into();
        store.save_memory(&changed, 1).unwrap();
        assert_eq!(store.get_memory("p1").unwrap().unwrap().objective, "Preserve project state");
        assert_eq!(store.list_projects().unwrap().projects.len(), 2);
    }

    #[test]
    fn save_requires_the_current_revision_and_archives_the_previous_one() {
        let (dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        let mut update = memory("p1");
        update.current_status = "Stage 1 done".into();
        let saved = store.save_memory(&update, 1).unwrap();
        assert_eq!(saved.revision, 2);

        // A proposal built on revision 1 is now stale.
        let mut stale = memory("p1");
        stale.current_status = "stale".into();
        assert!(store.save_memory(&stale, 1).unwrap_err().contains("conflict"));
        assert_eq!(store.get_memory("p1").unwrap().unwrap().current_status, "Stage 1 done");

        let archived = store.get_memory_revision("p1", 1).unwrap();
        assert_eq!(archived.revision, 1);
        assert_eq!(archived.current_status, "");
        assert_eq!(store.list_memory_revisions("p1").unwrap().iter().map(|r| r.revision).collect::<Vec<_>>(), vec![1]);
        assert!(dir.path().join("projects/p1/revisions/0000000001.json").exists());
    }

    #[test]
    fn a_normal_save_can_never_remove_items() {
        let (_dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        for strip in 0..4 {
            let mut update = memory("p1");
            match strip {
                0 => update.decisions.clear(),
                1 => update.work_items.clear(),
                2 => update.required_approvals.clear(),
                _ => update.sources.clear(),
            }
            assert!(store.save_memory(&update, 1).unwrap_err().contains("would remove"));
        }
        // Superseding instead of removing is fine.
        let mut update = memory("p1");
        update.decisions[0].status = DecisionStatus::Superseded;
        assert!(store.save_memory(&update, 1).is_ok());
        assert_eq!(store.get_memory("p1").unwrap().unwrap().revision, 2);
    }

    #[test]
    fn approval_changes_are_recorded_only() {
        let (_dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        let mut update = memory("p1");
        update.required_approvals[0].status = ApprovalStatus::Granted;
        let saved = store.save_memory(&update, 1).unwrap();
        assert_eq!(saved.required_approvals[0].status, ApprovalStatus::Granted);
    }

    #[test]
    fn verified_memory_items_need_verifying_evidence() {
        let (_dir, store) = setup();
        let mut m = memory("p1");
        m.work_items[0].status = EvidenceStatus::Verified;
        assert!(store.create_project(&m).is_err());
        m.work_items[0].evidence = vec![Evidence {
            kind: EvidenceKind::PriorReport,
            summary: "reported earlier".into(),
            observed_at: "2026-10-10T00:00:00.000Z".into(),
            session_id: "old".into(),
        }];
        assert!(store.create_project(&m).is_err());
        m.work_items[0].evidence[0].kind = EvidenceKind::TestRun;
        assert!(store.create_project(&m).is_ok());
    }

    #[test]
    fn keeps_only_the_latest_50_revisions_and_never_the_current_file() {
        let (dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        for revision in 1..=60u64 {
            let mut update = memory("p1");
            update.current_status = format!("step {revision}");
            store.save_memory(&update, revision).unwrap();
        }
        let current = store.get_memory("p1").unwrap().unwrap();
        assert_eq!(current.revision, 61);
        assert_eq!(current.current_status, "step 60");
        let revisions: Vec<u64> = store.list_memory_revisions("p1").unwrap().iter().map(|r| r.revision).collect();
        assert_eq!(revisions.len(), MEMORY_REVISIONS_KEPT);
        assert_eq!(revisions.first(), Some(&60));
        assert_eq!(revisions.last(), Some(&11));
        assert!(dir.path().join("projects/p1/memory.json").exists());
        // The oldest kept revision is still restorable.
        let restored = store.restore_memory_revision("p1", 11, Some(61), "2026-10-11T00:00:00.000Z").unwrap();
        assert_eq!(restored.revision, 62);
        assert_eq!(restored.current_status, "step 10");
    }

    #[test]
    fn restore_is_itself_a_reversible_revision() {
        let (_dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        let mut update = memory("p1");
        update.objective = "Changed".into();
        store.save_memory(&update, 1).unwrap();
        assert!(store.restore_memory_revision("p1", 1, Some(1), "t").unwrap_err().contains("conflict"));
        let restored = store.restore_memory_revision("p1", 1, Some(2), "2026-10-11T00:00:00.000Z").unwrap();
        assert_eq!((restored.revision, restored.objective.as_str()), (3, "Preserve project state"));
        // The replaced revision 2 is recoverable.
        assert_eq!(store.get_memory_revision("p1", 2).unwrap().objective, "Changed");
    }

    #[test]
    fn corrupt_memory_is_reported_never_overwritten_and_recoverable() {
        let (dir, store) = setup();
        store.create_project(&memory("p1")).unwrap();
        let mut update = memory("p1");
        update.objective = "Good".into();
        store.save_memory(&update, 1).unwrap();
        let path = dir.path().join("projects/p1/memory.json");
        fs::write(&path, b"{half a file").unwrap();

        assert!(store.get_memory("p1").unwrap_err().contains("unreadable"));
        let listed = store.list_projects().unwrap();
        assert!(listed.projects.is_empty());
        assert_eq!(listed.problems.len(), 1);
        assert!(store.save_memory(&update, 2).unwrap_err().contains("restore a revision"));
        assert_eq!(fs::read(&path).unwrap(), b"{half a file", "corrupt file untouched by save");
        assert!(store.restore_memory_revision("p1", 1, Some(2), "t").is_err());

        let recovered = store.restore_memory_revision("p1", 1, None, "2026-10-11T00:00:00.000Z").unwrap();
        assert_eq!(recovered.revision, 2);
        assert_eq!(recovered.objective, "Preserve project state");
        let quarantined: Vec<_> = fs::read_dir(dir.path().join("projects/p1"))
            .unwrap()
            .flatten()
            .filter(|e| e.file_name().to_string_lossy().starts_with("memory.corrupt-"))
            .collect();
        assert_eq!(quarantined.len(), 1);
        assert_eq!(fs::read(quarantined[0].path()).unwrap(), b"{half a file");
    }

    #[test]
    fn rejects_bad_ids_and_mismatched_files() {
        let (dir, store) = setup();
        for bad in ["../x", "a/b", ""] {
            assert!(store.get_memory(bad).is_err());
        }
        let mut other = memory("p2");
        store.create_project(&other).unwrap();
        fs::create_dir_all(dir.path().join("projects/p3")).unwrap();
        fs::copy(dir.path().join("projects/p2/memory.json"), dir.path().join("projects/p3/memory.json")).unwrap();
        assert!(store.get_memory("p3").unwrap_err().contains("belongs to project"));
        other.revision = 2;
        assert!(store.create_project(&memory("p9")).is_ok());
        other.project_id = "p4".into();
        assert!(store.create_project(&other).unwrap_err().contains("revision 1"));
    }
}
