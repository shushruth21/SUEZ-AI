//! Local checkpoint persistence: one JSON file per checkpoint under
//! `<data dir>/checkpoints/`, plus `<data dir>/settings.json`.
//! Writes go to a temp file first and are renamed into place, so a crash
//! mid-write never leaves a half-written checkpoint behind.
//!
//! Checkpoint schema versions: 1 (MVP and gateway phase) and 2 (adds
//! structured evidence; every new field has a default, so version 1 files
//! load unchanged). Files are never rewritten automatically; a version 1 file
//! becomes version 2 only when it is saved again, and its original bytes are
//! first copied to `checkpoints/.backups/`. Unsupported versions are refused.

use serde::{Deserialize, Serialize};
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageSnapshot {
    pub used_tokens: f64,
    pub limit_tokens: f64,
    pub percent: f64,
    pub source: String,
    pub simulated: bool,
    pub captured_at: String,
    /// "simulated", "provider_reported" or "gateway_estimate". Absent in MVP files.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub measurement_source: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cache_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub measured_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub gateway_session_id: Option<String>,
}

/// Evidence status of a piece of work. `Verified` needs evidence observed in
/// the checkpoint's own session; anything carried over is at most `Reported`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EvidenceStatus {
    Verified,
    Reported,
    Pending,
    Failed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EvidenceKind {
    TestRun,
    Command,
    Observation,
    UserConfirmation,
    /// A claim taken from an earlier session or report. Never enough to verify.
    PriorReport,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Evidence {
    pub kind: EvidenceKind,
    pub summary: String,
    pub observed_at: String,
    /// The session in which this evidence was observed.
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkItem {
    pub id: String,
    pub text: String,
    pub status: EvidenceStatus,
    #[serde(default)]
    pub evidence: Vec<Evidence>,
    /// Checkpoint this item was carried over from, if any.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub carried_from: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TestOutcome {
    Passed,
    Failed,
    NotRun,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Provenance {
    /// Run and observed in the checkpoint's own session.
    ThisSession,
    /// Copied from an earlier checkpoint; not rerun.
    Carried,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TestResult {
    pub id: String,
    pub suite: String,
    #[serde(default)]
    pub command: String,
    /// What the test actually covers; a pass verifies only this.
    #[serde(default)]
    pub covers: String,
    pub outcome: TestOutcome,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub passed: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failed: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub skipped: Option<u32>,
    pub observed_at: String,
    pub session_id: String,
    pub provenance: Provenance,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub carried_from: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CommandRecord {
    pub id: String,
    pub command: String,
    #[serde(default)]
    pub purpose: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i32>,
    pub at: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorStatus {
    Open,
    Resolved,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ErrorItem {
    pub id: String,
    pub error: String,
    #[serde(default)]
    pub resolution: String,
    pub status: ErrorStatus,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DecisionStatus {
    Active,
    Superseded,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DecisionRecord {
    pub id: String,
    pub text: String,
    #[serde(default)]
    pub rationale: String,
    pub decided_at: String,
    pub status: DecisionStatus,
}

#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Trigger {
    AutoThreshold,
    Manual,
}

#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    Draft,
    Final,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Checkpoint {
    pub schema_version: u32,
    pub id: String,
    pub created_at: String,
    pub updated_at: String,
    pub project_name: String,
    pub session_id: String,
    pub session_label: String,
    pub trigger: Trigger,
    pub threshold_percent: Option<f64>,
    pub status: Status,
    /// None when no trustworthy measurement existed at checkpoint time.
    pub usage: Option<UsageSnapshot>,
    pub goal: String,
    pub current_state: String,
    pub decisions: Vec<String>,
    pub completed: Vec<String>,
    pub open_tasks: Vec<String>,
    pub files_touched: Vec<String>,
    pub next_steps: Vec<String>,
    pub notes: String,
    // ---- Schema version 2 (all optional, so version 1 files load unchanged) ----
    /// Stable project id; absent on legacy checkpoints until explicitly assigned.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub project_id: Option<String>,
    #[serde(default)]
    pub current_task: String,
    #[serde(default)]
    pub next_action: String,
    #[serde(default)]
    pub work_items: Vec<WorkItem>,
    #[serde(default)]
    pub test_results: Vec<TestResult>,
    #[serde(default)]
    pub commands: Vec<CommandRecord>,
    #[serde(default)]
    pub errors: Vec<ErrorItem>,
    #[serde(default)]
    pub decision_records: Vec<DecisionRecord>,
    #[serde(default)]
    pub files_created: Vec<String>,
    #[serde(default)]
    pub files_modified: Vec<String>,
}

pub(crate) fn ensure_unique_ids<'a>(label: &str, ids: impl Iterator<Item = &'a str>) -> Result<(), String> {
    let mut seen = std::collections::HashSet::new();
    for id in ids {
        if id.trim().is_empty() {
            return Err(format!("{label} has an item without an id"));
        }
        if !seen.insert(id) {
            return Err(format!("{label} has a duplicate id {id:?}"));
        }
    }
    Ok(())
}

/// `Verified` needs at least one piece of evidence of a verifying kind observed
/// in `session_id`. Prior reports never verify anything.
pub fn check_verified(item: &WorkItem, session_id: Option<&str>) -> Result<(), String> {
    if item.status != EvidenceStatus::Verified {
        return Ok(());
    }
    let supported = item.evidence.iter().any(|e| {
        e.kind != EvidenceKind::PriorReport
            && !e.summary.trim().is_empty()
            && session_id.map_or(true, |session| e.session_id == session)
    });
    if supported {
        Ok(())
    } else {
        Err(format!(
            "work item {:?} is marked verified without supporting evidence from this session",
            item.text
        ))
    }
}

impl Checkpoint {
    pub fn validate(&self) -> Result<(), String> {
        if !SUPPORTED_SCHEMA_VERSIONS.contains(&self.schema_version) {
            return Err(format!("unsupported checkpoint schema version {}", self.schema_version));
        }
        validate_id(&self.id)?;
        if let Some(project_id) = &self.project_id {
            validate_id(project_id).map_err(|_| format!("invalid project id {project_id:?}"))?;
        }
        ensure_unique_ids("work items", self.work_items.iter().map(|i| i.id.as_str()))?;
        ensure_unique_ids("test results", self.test_results.iter().map(|t| t.id.as_str()))?;
        ensure_unique_ids("commands", self.commands.iter().map(|c| c.id.as_str()))?;
        ensure_unique_ids("errors", self.errors.iter().map(|e| e.id.as_str()))?;
        ensure_unique_ids("decisions", self.decision_records.iter().map(|d| d.id.as_str()))?;
        for item in &self.work_items {
            if item.text.trim().is_empty() {
                return Err("work items must have text".into());
            }
            check_verified(item, Some(&self.session_id))?;
        }
        for test in &self.test_results {
            if test.suite.trim().is_empty() {
                return Err("test results must name a suite".into());
            }
            match test.provenance {
                Provenance::ThisSession if test.session_id != self.session_id => {
                    return Err(format!(
                        "test result {:?} claims this session but was observed in session {:?}",
                        test.suite, test.session_id
                    ))
                }
                Provenance::Carried if test.carried_from.as_deref().map_or(true, |c| c.trim().is_empty()) => {
                    return Err(format!("carried test result {:?} must say which checkpoint it came from", test.suite))
                }
                _ => {}
            }
            if test.outcome == TestOutcome::Passed && test.failed.unwrap_or(0) > 0 {
                return Err(format!("test result {:?} is marked passed but reports failures", test.suite));
            }
        }
        Ok(())
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Settings {
    pub project_name: String,
    pub limit_tokens: f64,
    pub checkpoint_percent: f64,
    pub critical_percent: f64,
    pub rearm_margin: f64,
    /// "mock" or "gateway". The API key is never stored here (see gateway.rs).
    #[serde(default = "default_usage_source")]
    pub usage_source: String,
    #[serde(default)]
    pub gateway_url: String,
    #[serde(default)]
    pub gateway_session_id: String,
    #[serde(default = "default_poll_interval")]
    pub poll_interval_seconds: f64,
    /// Stable id of the selected project (see memory.rs). Empty = none selected;
    /// `project_name` then applies to legacy, name-only checkpoints.
    #[serde(default)]
    pub active_project_id: String,
}

fn default_usage_source() -> String {
    "mock".to_string()
}

fn default_poll_interval() -> f64 {
    5.0
}

impl Settings {
    pub fn validate(&self) -> Result<(), String> {
        if self.usage_source != "mock" && self.usage_source != "gateway" {
            return Err(format!("unknown usage source {:?}", self.usage_source));
        }
        if !(1.0..=300.0).contains(&self.poll_interval_seconds) {
            return Err("poll interval must be between 1 and 300 seconds".into());
        }
        if !self.active_project_id.is_empty() && validate_id(&self.active_project_id).is_err() {
            return Err("invalid active project id".into());
        }
        if !self.gateway_session_id.is_empty() && validate_id(&self.gateway_session_id).is_err() {
            return Err("gateway session id must use only letters, digits, - and _".into());
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ListResult {
    pub checkpoints: Vec<Checkpoint>,
    /// File names that could not be parsed; they are left on disk untouched.
    pub skipped: Vec<String>,
}

/// Version written by this build.
pub const SCHEMA_VERSION: u32 = 2;
/// Versions this build can read. Anything else is refused and left untouched.
pub const SUPPORTED_SCHEMA_VERSIONS: [u32; 2] = [1, 2];

pub struct Store {
    root: PathBuf,
}

impl Store {
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self { root: root.into() }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    fn checkpoint_dir(&self) -> PathBuf {
        self.root.join("checkpoints")
    }

    fn checkpoint_path(&self, id: &str) -> Result<PathBuf, String> {
        validate_id(id)?;
        Ok(self.checkpoint_dir().join(format!("{id}.json")))
    }

    fn backup_dir(&self) -> PathBuf {
        self.checkpoint_dir().join(".backups")
    }

    /// Validates, then writes the checkpoint as the current schema version and
    /// returns what was stored. If the file on disk is a supported older
    /// version, its exact bytes are backed up (and the backup verified) first.
    /// An unreadable or unsupported file on disk is refused, never overwritten.
    pub fn save(&self, checkpoint: &Checkpoint) -> Result<Checkpoint, String> {
        checkpoint.validate()?;
        let path = self.checkpoint_path(&checkpoint.id)?;
        match fs::read(&path) {
            Ok(existing) => {
                let version = schema_version_of(&existing).ok_or_else(|| {
                    format!(
                        "checkpoint {} on disk is unreadable; refusing to overwrite it",
                        checkpoint.id
                    )
                })?;
                if !SUPPORTED_SCHEMA_VERSIONS.contains(&version) {
                    return Err(format!(
                        "checkpoint {} on disk uses unsupported schema version {version}; refusing to overwrite it",
                        checkpoint.id
                    ));
                }
                if version < SCHEMA_VERSION {
                    self.backup_legacy(&checkpoint.id, version, &existing)?;
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.to_string()),
        }
        let mut stored = checkpoint.clone();
        stored.schema_version = SCHEMA_VERSION;
        let payload = serde_json::to_vec_pretty(&stored).map_err(|e| e.to_string())?;
        write_atomic(&path, &payload)?;
        match self.get(&stored.id)? {
            Some(read_back) if read_back == stored => Ok(stored),
            _ => Err(format!("checkpoint {} did not read back correctly after saving", stored.id)),
        }
    }

    fn backup_legacy(&self, id: &str, version: u32, bytes: &[u8]) -> Result<PathBuf, String> {
        let path = self.backup_dir().join(format!("{id}.v{version}.{}.json", unix_millis()));
        write_atomic(&path, bytes)?;
        if fs::read(&path).map_err(|e| e.to_string())? != bytes {
            return Err(format!("backup of checkpoint {id} could not be verified; nothing was changed"));
        }
        Ok(path)
    }

    pub fn get(&self, id: &str) -> Result<Option<Checkpoint>, String> {
        let path = self.checkpoint_path(id)?;
        match fs::read(&path) {
            Ok(bytes) => parse_checkpoint(&bytes)
                .map(Some)
                .map_err(|e| format!("checkpoint {id} is unreadable: {e}")),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e.to_string()),
        }
    }

    pub fn delete(&self, id: &str) -> Result<bool, String> {
        let path = self.checkpoint_path(id)?;
        match fs::remove_file(&path) {
            Ok(()) => Ok(true),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(false),
            Err(e) => Err(e.to_string()),
        }
    }

    /// All readable checkpoints, newest first.
    pub fn list(&self) -> Result<ListResult, String> {
        let dir = self.checkpoint_dir();
        let entries = match fs::read_dir(&dir) {
            Ok(entries) => entries,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                return Ok(ListResult { checkpoints: vec![], skipped: vec![] })
            }
            Err(e) => return Err(e.to_string()),
        };

        let mut checkpoints = Vec::new();
        let mut skipped = Vec::new();
        for entry in entries.flatten() {
            let path = entry.path();
            let name = entry.file_name().to_string_lossy().to_string();
            if name.starts_with('.') || path.extension().and_then(|e| e.to_str()) != Some("json") {
                continue;
            }
            match fs::read(&path)
                .map_err(|e| e.to_string())
                .and_then(|bytes| parse_checkpoint(&bytes))
            {
                Ok(checkpoint) => checkpoints.push(checkpoint),
                Err(_) => skipped.push(name),
            }
        }
        checkpoints.sort_by(|a, b| b.created_at.cmp(&a.created_at).then_with(|| b.id.cmp(&a.id)));
        skipped.sort();
        Ok(ListResult { checkpoints, skipped })
    }

    pub fn load_settings(&self) -> Result<Option<Settings>, String> {
        match fs::read(self.root.join("settings.json")) {
            Ok(bytes) => serde_json::from_slice(&bytes)
                .map(Some)
                .map_err(|e| format!("settings.json is unreadable: {e}")),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e.to_string()),
        }
    }

    pub fn save_settings(&self, settings: &Settings) -> Result<(), String> {
        settings.validate()?;
        let payload = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
        write_atomic(&self.root.join("settings.json"), &payload)
    }
}

fn schema_version_of(bytes: &[u8]) -> Option<u32> {
    let value: serde_json::Value = serde_json::from_slice(bytes).ok()?;
    value.get("schemaVersion")?.as_u64().and_then(|v| u32::try_from(v).ok())
}

fn parse_checkpoint(bytes: &[u8]) -> Result<Checkpoint, String> {
    match schema_version_of(bytes) {
        Some(version) if SUPPORTED_SCHEMA_VERSIONS.contains(&version) => {
            serde_json::from_slice(bytes).map_err(|e| e.to_string())
        }
        Some(version) => Err(format!("unsupported schema version {version}")),
        None => Err("not a checkpoint file".into()),
    }
}

pub(crate) fn unix_millis() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

/// Ids become file names, so only allow a conservative character set.
pub fn validate_id(id: &str) -> Result<(), String> {
    let ok = !id.is_empty()
        && id.len() <= 64
        && id.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_');
    if ok {
        Ok(())
    } else {
        Err(format!("invalid checkpoint id {id:?}"))
    }
}

pub(crate) fn write_atomic(path: &Path, payload: &[u8]) -> Result<(), String> {
    let parent = path.parent().ok_or("path has no parent directory")?;
    fs::create_dir_all(parent).map_err(|e| e.to_string())?;
    let file_name = path.file_name().ok_or("path has no file name")?.to_string_lossy();
    let tmp = parent.join(format!(".{file_name}.tmp"));
    {
        let mut file = fs::File::create(&tmp).map_err(|e| e.to_string())?;
        file.write_all(payload).map_err(|e| e.to_string())?;
        file.sync_all().map_err(|e| e.to_string())?;
    }
    fs::rename(&tmp, path).map_err(|e| e.to_string())
}

#[cfg(test)]
mod legacy_checkpoint_tests {
    //! Regression tests against a synthetic schema-1 checkpoint that mirrors the
    //! structure of real hand-written checkpoints. The fixture is copied into a
    //! temporary directory; no user file is ever touched.
    use super::*;
    use serde_json::Value;

    const FIXTURE: &str = include_str!("testdata/checkpoint-v1-sample.json");
    const ID: &str = "example-cg-v1-legacy-sample";

    fn store_with_copy() -> (tempfile::TempDir, Store, PathBuf) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("checkpoints").join(format!("{ID}.json"));
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, FIXTURE).unwrap();
        let store = Store::new(dir.path());
        (dir, store, path)
    }

    /// Every key and value of `original` must appear unchanged in `loaded`.
    fn assert_preserved(original: &Value, loaded: &Value) {
        let (Value::Object(orig), Value::Object(got)) = (original, loaded) else { panic!("not objects") };
        for (key, value) in orig {
            assert_eq!(got.get(key), Some(value), "field {key} changed or lost");
        }
    }

    #[test]
    fn v1_checkpoint_loads_with_every_field_preserved() {
        let (_dir, store, path) = store_with_copy();
        let checkpoint = store.get(ID).unwrap().expect("checkpoint readable");
        let original: Value = serde_json::from_str(FIXTURE).unwrap();
        assert_preserved(&original, &serde_json::to_value(&checkpoint).unwrap());
        assert_eq!(checkpoint.decisions.len(), 4);
        assert_eq!(checkpoint.completed.len(), 6);
        assert_eq!(checkpoint.open_tasks.len(), 10);
        assert_eq!(checkpoint.files_touched.len(), 3);
        assert_eq!(checkpoint.next_steps.len(), 3);
        assert!(checkpoint.usage.is_none());
        // Reading never writes.
        assert_eq!(fs::read_to_string(&path).unwrap(), FIXTURE);
    }

    #[test]
    fn v1_checkpoint_is_listed_in_history_and_left_unchanged() {
        let (_dir, store, path) = store_with_copy();
        let listed = store.list().unwrap();
        assert_eq!(listed.skipped, Vec::<String>::new());
        assert_eq!(listed.checkpoints.iter().map(|c| c.id.as_str()).collect::<Vec<_>>(), vec![ID]);
        assert_eq!(fs::read_to_string(&path).unwrap(), FIXTURE);
    }

    fn backups(dir: &Path) -> Vec<PathBuf> {
        let mut found: Vec<PathBuf> = fs::read_dir(dir.join("checkpoints/.backups"))
            .map(|entries| entries.flatten().map(|e| e.path()).collect())
            .unwrap_or_default();
        found.sort();
        found
    }

    #[test]
    fn editing_the_v1_checkpoint_backs_it_up_and_upgrades_without_losing_anything() {
        let (dir, store, path) = store_with_copy();
        let mut checkpoint = store.get(ID).unwrap().unwrap();
        checkpoint.project_id = Some("project-1".to_string());
        let saved = store.save(&checkpoint).unwrap();
        assert_eq!(saved.schema_version, 2);

        // The exact original bytes are recoverable from the backup.
        let found = backups(dir.path());
        assert_eq!(found.len(), 1);
        assert!(found[0].file_name().unwrap().to_string_lossy().starts_with(&format!("{ID}.v1.")));
        assert_eq!(fs::read_to_string(&found[0]).unwrap(), FIXTURE);

        // The upgraded file still holds every original field and value.
        let original: Value = serde_json::from_str(FIXTURE).unwrap();
        let upgraded: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        let mut expected = original.clone();
        expected["schemaVersion"] = Value::from(2);
        assert_preserved(&expected, &upgraded);
        assert_eq!(upgraded["projectId"], "project-1");

        // A second save of a v2 file makes no further backups; backups stay out of history.
        store.save(&saved).unwrap();
        assert_eq!(backups(dir.path()).len(), 1);
        assert_eq!(store.list().unwrap().checkpoints.len(), 1);
        assert!(store.delete(ID).unwrap());
        assert_eq!(fs::read_to_string(&found[0]).unwrap(), FIXTURE, "backup survives delete");
    }

    /// Run explicitly against one of your own checkpoint files (read-only, via a
    /// temp copy); never commit real checkpoints as fixtures:
    /// CG_REAL_CHECKPOINT=<path> cargo test real_file -- --ignored
    #[test]
    #[ignore]
    fn real_file_loads_losslessly_and_is_unchanged() {
        let real = std::env::var("CG_REAL_CHECKPOINT").expect("set CG_REAL_CHECKPOINT");
        let before = fs::read(&real).unwrap();
        let id = serde_json::from_slice::<Value>(&before).unwrap()["id"].as_str().unwrap().to_string();
        let dir = tempfile::tempdir().unwrap();
        let copy = dir.path().join("checkpoints").join(format!("{id}.json"));
        fs::create_dir_all(copy.parent().unwrap()).unwrap();
        fs::copy(&real, &copy).unwrap();
        let loaded = Store::new(dir.path()).get(&id).unwrap().unwrap();
        let original: Value = serde_json::from_slice(&before).unwrap();
        assert_preserved(&original, &serde_json::to_value(&loaded).unwrap());
        assert_eq!(fs::read(&real).unwrap(), before, "real file must be unchanged");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample(id: &str, created_at: &str) -> Checkpoint {
        Checkpoint {
            schema_version: SCHEMA_VERSION,
            id: id.to_string(),
            created_at: created_at.to_string(),
            updated_at: created_at.to_string(),
            project_name: "SUEZ".to_string(),
            session_id: "s-1".to_string(),
            session_label: "Session 1".to_string(),
            trigger: Trigger::AutoThreshold,
            threshold_percent: Some(85.0),
            status: Status::Draft,
            usage: Some(UsageSnapshot {
                used_tokens: 170_000.0,
                limit_tokens: 200_000.0,
                percent: 85.0,
                source: "mock".to_string(),
                simulated: true,
                captured_at: created_at.to_string(),
                measurement_source: None,
                cache_status: None,
                measured_at: None,
                model: None,
                gateway_session_id: None,
            }),
            goal: "Ship MVP".to_string(),
            current_state: String::new(),
            decisions: vec!["JSON files".to_string()],
            completed: vec![],
            open_tasks: vec!["History".to_string()],
            files_touched: vec![],
            next_steps: vec![],
            notes: String::new(),
            project_id: None,
            current_task: String::new(),
            next_action: String::new(),
            work_items: vec![],
            test_results: vec![],
            commands: vec![],
            errors: vec![],
            decision_records: vec![],
            files_created: vec![],
            files_modified: vec![],
        }
    }

    #[test]
    fn save_get_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let checkpoint = sample("abc-123", "2026-10-09T12:00:00.000Z");
        store.save(&checkpoint).unwrap();
        assert_eq!(store.get("abc-123").unwrap(), Some(checkpoint));
        assert_eq!(store.get("missing").unwrap(), None);
    }

    #[test]
    fn json_uses_camel_case_matching_the_frontend() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        store.save(&sample("x1", "2026-10-09T12:00:00.000Z")).unwrap();
        let raw = fs::read_to_string(dir.path().join("checkpoints/x1.json")).unwrap();
        for key in ["\"schemaVersion\"", "\"openTasks\"", "\"usedTokens\"", "\"auto_threshold\"", "\"draft\""] {
            assert!(raw.contains(key), "missing {key} in {raw}");
        }
    }

    #[test]
    fn save_overwrites_existing_and_leaves_no_temp_file() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut checkpoint = sample("a", "2026-10-09T12:00:00.000Z");
        store.save(&checkpoint).unwrap();
        checkpoint.status = Status::Final;
        checkpoint.goal = "Updated".to_string();
        store.save(&checkpoint).unwrap();
        assert_eq!(store.get("a").unwrap().unwrap().status, Status::Final);
        let names: Vec<_> = fs::read_dir(dir.path().join("checkpoints"))
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().to_string())
            .collect();
        assert_eq!(names, vec!["a.json"]);
    }

    #[test]
    fn list_is_newest_first_and_skips_corrupt_files() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        store.save(&sample("old", "2026-10-01T00:00:00.000Z")).unwrap();
        store.save(&sample("new", "2026-10-02T00:00:00.000Z")).unwrap();
        fs::write(dir.path().join("checkpoints/broken.json"), b"{not json").unwrap();
        fs::write(dir.path().join("checkpoints/readme.txt"), b"ignored").unwrap();

        let result = store.list().unwrap();
        let ids: Vec<_> = result.checkpoints.iter().map(|c| c.id.as_str()).collect();
        assert_eq!(ids, vec!["new", "old"]);
        assert_eq!(result.skipped, vec!["broken.json"]);
        assert!(dir.path().join("checkpoints/broken.json").exists(), "corrupt file must be kept");
    }

    #[test]
    fn list_on_empty_store_is_empty() {
        let dir = tempfile::tempdir().unwrap();
        let result = Store::new(dir.path().join("never-created")).list().unwrap();
        assert!(result.checkpoints.is_empty() && result.skipped.is_empty());
    }

    #[test]
    fn rejects_ids_that_could_escape_the_directory() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        for bad in ["", "../evil", "a/b", "a.json", "x".repeat(65).as_str()] {
            assert!(validate_id(bad).is_err(), "{bad:?} should be rejected");
            assert!(store.get(bad).is_err());
            assert!(store.delete(bad).is_err());
        }
        assert!(validate_id("123e4567-e89b-12d3-a456-426614174000").is_ok());
    }

    #[test]
    fn rejects_unknown_schema_version() {
        let dir = tempfile::tempdir().unwrap();
        let mut checkpoint = sample("v", "2026-10-09T12:00:00.000Z");
        checkpoint.schema_version = 99;
        assert!(Store::new(dir.path()).save(&checkpoint).is_err());
    }

    #[test]
    fn unsupported_or_unreadable_files_on_disk_are_refused_and_left_untouched() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        fs::create_dir_all(dir.path().join("checkpoints")).unwrap();
        let future = br#"{"schemaVersion":3,"id":"future","somethingNew":true}"#;
        fs::write(dir.path().join("checkpoints/future.json"), future).unwrap();
        fs::write(dir.path().join("checkpoints/broken.json"), b"{not json").unwrap();

        assert!(store.get("future").unwrap_err().contains("unsupported schema version 3"));
        let listed = store.list().unwrap();
        assert!(listed.checkpoints.is_empty());
        assert_eq!(listed.skipped, vec!["broken.json", "future.json"]);

        for id in ["future", "broken"] {
            let err = store.save(&sample(id, "2026-10-09T12:00:00.000Z")).unwrap_err();
            assert!(err.contains("refusing to overwrite"), "{err}");
        }
        assert_eq!(fs::read(dir.path().join("checkpoints/future.json")).unwrap(), future);
        assert_eq!(fs::read(dir.path().join("checkpoints/broken.json")).unwrap(), b"{not json");
    }

    #[test]
    fn version_1_input_is_stored_as_version_2() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut checkpoint = sample("new", "2026-10-09T12:00:00.000Z");
        checkpoint.schema_version = 1;
        assert_eq!(store.save(&checkpoint).unwrap().schema_version, 2);
        assert_eq!(store.get("new").unwrap().unwrap().schema_version, 2);
    }

    fn evidence(kind: EvidenceKind, session: &str) -> Evidence {
        Evidence {
            kind,
            summary: "npm test: 54 passed".to_string(),
            observed_at: "2026-10-10T12:00:00.000Z".to_string(),
            session_id: session.to_string(),
        }
    }

    fn item(status: EvidenceStatus, evidence: Vec<Evidence>) -> WorkItem {
        WorkItem { id: "w1".to_string(), text: "Tests pass".to_string(), status, evidence, carried_from: None }
    }

    #[test]
    fn verified_requires_current_session_evidence_of_a_verifying_kind() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut checkpoint = sample("ev", "2026-10-10T12:00:00.000Z"); // session "s-1"
        let cases = [
            (item(EvidenceStatus::Verified, vec![]), false),
            (item(EvidenceStatus::Verified, vec![evidence(EvidenceKind::PriorReport, "s-1")]), false),
            (item(EvidenceStatus::Verified, vec![evidence(EvidenceKind::TestRun, "older-session")]), false),
            (item(EvidenceStatus::Verified, vec![evidence(EvidenceKind::TestRun, "s-1")]), true),
            (item(EvidenceStatus::Verified, vec![evidence(EvidenceKind::UserConfirmation, "s-1")]), true),
            (item(EvidenceStatus::Reported, vec![evidence(EvidenceKind::PriorReport, "older-session")]), true),
            (item(EvidenceStatus::Pending, vec![]), true),
            (item(EvidenceStatus::Failed, vec![]), true),
        ];
        for (work, ok) in cases {
            checkpoint.work_items = vec![work.clone()];
            assert_eq!(store.save(&checkpoint).is_ok(), ok, "{work:?}");
        }
    }

    fn test_result(provenance: Provenance, session: &str, carried_from: Option<&str>) -> TestResult {
        TestResult {
            id: "t1".to_string(),
            suite: "vitest".to_string(),
            command: "npx vitest run".to_string(),
            covers: "frontend unit tests".to_string(),
            outcome: TestOutcome::Passed,
            passed: Some(54),
            failed: Some(0),
            skipped: None,
            observed_at: "2026-10-10T12:00:00.000Z".to_string(),
            session_id: session.to_string(),
            provenance,
            carried_from: carried_from.map(str::to_string),
        }
    }

    #[test]
    fn test_results_must_be_honest_about_provenance() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut checkpoint = sample("tr", "2026-10-10T12:00:00.000Z");
        let cases = [
            (test_result(Provenance::ThisSession, "s-1", None), true),
            (test_result(Provenance::ThisSession, "older-session", None), false),
            (test_result(Provenance::Carried, "older-session", Some("cp-0")), true),
            (test_result(Provenance::Carried, "older-session", None), false),
        ];
        for (result, ok) in cases {
            checkpoint.test_results = vec![result.clone()];
            assert_eq!(store.save(&checkpoint).is_ok(), ok, "{result:?}");
        }
        let mut contradictory = test_result(Provenance::ThisSession, "s-1", None);
        contradictory.failed = Some(2);
        checkpoint.test_results = vec![contradictory];
        assert!(store.save(&checkpoint).is_err());
    }

    #[test]
    fn duplicate_or_missing_item_ids_are_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut checkpoint = sample("dup", "2026-10-10T12:00:00.000Z");
        checkpoint.work_items = vec![item(EvidenceStatus::Pending, vec![]), item(EvidenceStatus::Pending, vec![])];
        assert!(store.save(&checkpoint).unwrap_err().contains("duplicate id"));
        checkpoint.work_items = vec![WorkItem { id: " ".into(), ..item(EvidenceStatus::Pending, vec![]) }];
        assert!(store.save(&checkpoint).is_err());
    }

    #[test]
    fn delete_reports_whether_anything_was_removed() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        store.save(&sample("d", "2026-10-09T12:00:00.000Z")).unwrap();
        assert!(store.delete("d").unwrap());
        assert!(!store.delete("d").unwrap());
    }

    #[test]
    fn settings_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        assert_eq!(store.load_settings().unwrap(), None);
        let settings = Settings {
            project_name: "SUEZ".to_string(),
            limit_tokens: 200_000.0,
            checkpoint_percent: 85.0,
            critical_percent: 95.0,
            rearm_margin: 5.0,
            usage_source: "gateway".to_string(),
            gateway_url: "http://localhost:8080".to_string(),
            gateway_session_id: "abc-123".to_string(),
            poll_interval_seconds: 5.0,
            active_project_id: "project-1".to_string(),
        };
        store.save_settings(&settings).unwrap();
        assert_eq!(store.load_settings().unwrap(), Some(settings));
    }

    #[test]
    fn mvp_settings_file_without_gateway_fields_still_loads() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(
            dir.path().join("settings.json"),
            br#"{"projectName":"SUEZ","limitTokens":200000,"checkpointPercent":85,"criticalPercent":95,"rearmMargin":5}"#,
        )
        .unwrap();
        let settings = Store::new(dir.path()).load_settings().unwrap().unwrap();
        assert_eq!(settings.usage_source, "mock");
        assert_eq!(settings.gateway_url, "");
        assert_eq!(settings.poll_interval_seconds, 5.0);
    }

    #[test]
    fn rejects_invalid_settings() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        let mut settings = Settings {
            project_name: "SUEZ".to_string(),
            limit_tokens: 200_000.0,
            checkpoint_percent: 85.0,
            critical_percent: 95.0,
            rearm_margin: 5.0,
            usage_source: "telepathy".to_string(),
            gateway_url: String::new(),
            gateway_session_id: String::new(),
            poll_interval_seconds: 5.0,
            active_project_id: String::new(),
        };
        assert!(store.save_settings(&settings).is_err());
        settings.usage_source = "gateway".to_string();
        settings.poll_interval_seconds = 0.0;
        assert!(store.save_settings(&settings).is_err());
        settings.poll_interval_seconds = 5.0;
        settings.gateway_session_id = "../x".to_string();
        assert!(store.save_settings(&settings).is_err());
        settings.gateway_session_id = String::new();
        settings.active_project_id = "../p".to_string();
        assert!(store.save_settings(&settings).is_err());
    }

    #[test]
    fn mvp_checkpoint_file_still_loads_and_gateway_usage_round_trips() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::new(dir.path());
        // Shape written by the MVP (no gateway fields).
        let mvp = r#"{"schemaVersion":1,"id":"old","createdAt":"2026-10-09T12:00:00.000Z","updatedAt":"2026-10-09T12:00:00.000Z","projectName":"SUEZ","sessionId":"s","sessionLabel":"S","trigger":"auto_threshold","thresholdPercent":85,"status":"draft","usage":{"usedTokens":170000,"limitTokens":200000,"percent":85,"source":"mock","simulated":true,"capturedAt":"2026-10-09T12:00:00.000Z"},"goal":"","currentState":"","decisions":[],"completed":[],"openTasks":[],"filesTouched":[],"nextSteps":[],"notes":""}"#;
        fs::create_dir_all(dir.path().join("checkpoints")).unwrap();
        fs::write(dir.path().join("checkpoints/old.json"), mvp).unwrap();
        let old = store.get("old").unwrap().unwrap();
        assert_eq!(old.usage.as_ref().unwrap().measurement_source, None);

        let mut gateway = sample("gw", "2026-10-09T13:00:00.000Z");
        gateway.usage = Some(UsageSnapshot {
            used_tokens: 860.0,
            limit_tokens: 1000.0,
            percent: 86.0,
            source: "gateway".to_string(),
            simulated: false,
            captured_at: "2026-10-09T13:00:00.000Z".to_string(),
            measurement_source: Some("provider_reported".to_string()),
            cache_status: Some("hit".to_string()),
            measured_at: Some("2026-10-09T12:59:59Z".to_string()),
            model: Some("groq-llama".to_string()),
            gateway_session_id: Some("abc".to_string()),
        });
        store.save(&gateway).unwrap();
        assert_eq!(store.get("gw").unwrap(), Some(gateway.clone()));

        gateway.usage = None;
        store.save(&gateway).unwrap();
        assert_eq!(store.get("gw").unwrap().unwrap().usage, None);
    }
}
