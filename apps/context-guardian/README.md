# SUEZ Context Guardian

A small, always-on-top desktop widget that watches AI context usage, auto-saves
a structured **checkpoint draft at 85%**, warns at **95%**, keeps a local
checkpoint history, and generates a **continuation prompt** you can paste into a
fresh AI session.

Built with Tauri 2 (Rust) + React 18 + TypeScript + Vite. It runs outside the
Docker Compose stack and has two usage sources, chosen in Settings (⚙):

- **Mock** (default): simulated usage, labelled `MOCK DATA` everywhere
  (title bar, monitor, saved checkpoints with `usage.simulated: true`, and every
  continuation prompt). It is not real telemetry.
- **SUEZ gateway**: usage measured by the SUEZ gateway for one session id (see
  [Gateway mode](#gateway-mode)). The title bar shows `GATEWAY`, plus
  `ESTIMATE` when the counts come from the stub model, and `NO DATA` or `STALE`
  when there is no trustworthy current measurement.

The app never switches between sources by itself. If the gateway fails, it
stays on the gateway source and says so.

## Features

| Feature | Where |
| --- | --- |
| Frameless, draggable (title bar), always-on-top floating window, visible on all Spaces | `src-tauri/tauri.conf.json`, `src/App.tsx` |
| Mock context usage: slider, +5%, Simulate (2% / second), New session | `src/usage/mockSource.ts`, `src/components/Monitor.tsx` |
| Configurable checkpoint (85%) and critical (95%) thresholds with re-arm margin (5%) | `src/guardian/threshold.ts`, Settings (⚙) |
| Automatic draft checkpoint at the threshold, editor opens immediately | `src/App.tsx` |
| Structured editor with debounced autosave, Mark final / Reopen, Delete | `src/components/CheckpointEditor.tsx` |
| Local persistent storage (one JSON file per checkpoint, atomic writes) | `src-tauri/src/storage.rs` |
| History (newest first, per project or all projects) | `src/components/HistoryList.tsx` |
| Continuation prompt preview + Copy to clipboard | `src/guardian/prompt.ts` |
| Gateway usage source (polling via Rust, stale/unavailable states) | `src/usage/gatewaySource.ts`, `src/usage/gatewayReading.ts`, `src-tauri/src/gateway.rs` |
| Gateway URL, poll interval and API key settings; key kept in the macOS Keychain | `src/components/SettingsPanel.tsx`, `src-tauri/src/gateway.rs` |

### Trigger rules

- The **checkpoint** event fires once when usage reaches the checkpoint
  threshold. It saves a `draft` checkpoint immediately (so nothing is lost even
  if you ignore it) and opens the editor.
- The **critical** event fires once at the critical threshold, shows a red
  banner, pauses the simulation and requests window attention.
- Each event re-arms only after usage drops below `threshold − margin`
  (default 80% / 90%), so jitter around the line cannot create duplicate drafts.
  **New session** resets usage to 0 and re-arms both.
- A jump straight past both thresholds fires checkpoint, then critical.

### What a new draft contains

With a project selected (◆), a draft belongs to that project by its stable id.
It is built from the project's memory (objective, next action, outstanding
work) plus the newest checkpoint of the same project. Goal, decisions,
completed items, open tasks, files touched, next steps, the current task,
structured work items, test results, open errors and decisions are carried
forward. Current state, notes, commands and file lists start empty because
they describe one session only.

**Carried claims are never verified.** A carried `verified` work item becomes
`reported`, with a `prior_report` evidence entry naming the checkpoint and
session it came from. Carried test results are marked `carried`, "NOT rerun".

Without a project selected, the app works as before: drafts carry forward from
the newest *unassigned* checkpoint whose name matches Settings → Project name.

The Guardian cannot see your AI conversation, so the content is written by you
(or pasted from your AI session) in the editor. It is not summarised automatically.

After an app restart, a gateway session that already has an automatic draft
does not get a second one. The trigger starts disarmed and re-arms normally once
usage drops below the re-arm level.

## Project memory

One persistent, versioned memory record per project, independent of any single
checkpoint or AI conversation. It holds the objective, requirements,
constraints, architecture, environment facts (never secrets), decisions with
status, work items (`verified` / `reported` / `pending` / `failed`), test
results, the current status, known issues, required approvals, the exact next
action, notes, and which checkpoints fed it.

- **Identity.** Projects have a stable id. Names are display only, so two
  projects with the same name are never merged. Older checkpoints join a project
  only by an explicit **Assign to project** or **Create project from this
  checkpoint** (import).
- **Every update is reviewed.** Import, merge ("Propose project-memory update"
  on a *final* checkpoint) and manual edits all show a preview of every change
  first. Nothing is written until you click **Approve and save**.
- **Automatic checkpoints never change memory.** The 85% trigger only creates
  a checkpoint draft.
- **Revision checks.** A preview remembers the revision it was based on. If
  memory changed in the meantime, the save is refused and you review again.
- **Nothing is removed silently.** Items with ids (decisions, work items, test
  results, approvals, sources) cannot be removed by a save; mark them
  superseded or resolved. Lines removed from text lists are shown in the preview.
- **History.** Before every save the previous memory is copied to
  `revisions/` and verified; the latest 50 revisions are kept. Restoring a
  revision creates a new revision, so a restore is itself undoable.
- **Unreadable memory** is reported and never overwritten. Recovery from a
  revision renames the bad file to `memory.corrupt-<time>.json`; it is never
  deleted.
- **Approvals are records only.** Marking an approval granted (for example
  "apply migration 0006") never performs the operation.
- **Evidence rules (enforced in Rust).** `verified` needs evidence of a
  verifying kind (test run, command output, observation, user confirmation)
  recorded in the checkpoint's own session. A `prior_report` never verifies
  anything. A test result marked "this session" must carry that session's id,
  and a passed result cannot report failures. A passing test verifies only what
  it covers.
- **Secrets.** Rust refuses to save a checkpoint or memory, and refuses to copy
  a prompt, if it contains the gateway API key stored in the Keychain. If the
  Keychain cannot be read, nothing can be compared and the save goes ahead.

### Continuation prompt

For a checkpoint with structured content or project memory, the prompt starts
with **NEXT ACTION**. It then separates work verified in the checkpoint's
session from reported claims, failed and pending work. Test results appear with
provenance and coverage, alongside commands, errors, decisions with rationale
and files. A capped summary of project memory follows: constraints, active
decisions, approvals still awaiting (with a do-not-perform instruction), known
issues and outstanding work. Lists are capped at 12 items, so the prompt does
not paste the whole history. A checkpoint with neither produces exactly the
same prompt as before (checked byte-for-byte in a test).

## Gateway mode

### What the percentage measures

Chat clients resend the whole conversation on every request. The context
occupancy is therefore the **latest** request's prompt plus completion tokens
for the session, divided by the model's context window. Requests are never
summed. The gateway only reports a percentage when:

- the latest request for the session reported token usage (`unavailable`
  otherwise), and
- the model has `models.context_window` set (`unknown_context_window`
  otherwise; token counts are still shown).

Each measurement has a `source`:

| source | meaning |
| --- | --- |
| `provider_reported` | OpenAI-compatible / Groq returned `usage` |
| `gateway_estimate` | stub model: the gateway's own estimate (characters ÷ 4), not provider telemetry |
| `unavailable` | the provider returned no usage, so the size is unknown |

Cache hits are recorded with `cacheStatus: "hit"`. The cache key covers the
full message list, so the cached counts describe exactly this conversation.
Without this, the previous, smaller measurement would be shown as current.

Only a fresh `ok` reading reaches the threshold engine. No measurement yet,
unavailable usage, an unknown window, auth or network errors, and readings
older than three poll intervals do not. Repeated readings above a threshold
never create another draft.

### Only conversations sent through SUEZ

The Guardian can only measure chat requests that go **through the SUEZ
gateway** with the session header. It cannot see ChatGPT, Claude.ai or other
clients that talk to providers directly.

### Setup

1. Apply migration `0006` to your database and rebuild the gateway (see
   "Database migration" below).
2. Give the models a context window (example for the stub model):
   ```sh
   docker exec -i suez-postgres-1 sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
   UPDATE models SET context_window = 8192 WHERE external_name = 'demo-model';
   SQL
   ```
   Use the real limit for each provider model (check the provider's docs).
3. In the app: ⚙ → **SUEZ gateway (measured)**, set the gateway URL (default
   `http://localhost:8080`), **Save settings**, paste the API key → **Save
   key** → **Test connection**.
4. On the monitor, **Copy header** copies `X-SUEZ-Session-Id: <id>`. Send that
   header with every `/v1/chat/completions` request of the conversation.
   **New session** starts watching a new id.

### API key storage

The key is sent to the Rust side once and stored in the **macOS Keychain**
(service `ai.acrosystems.suez.contextguardian`, account `gateway-api-key`).
It is never returned to the UI and never written to `settings.json`,
checkpoints, logs or error messages. Gateway requests are made from Rust with
a 5 s timeout. After a rebuild, macOS may ask once whether the new build may
read the key, because the app has only an ad-hoc signature.

### Gateway endpoint

`GET /v1/context/usage?session=<id>` uses the same API-key / JWT auth as the
other `/v1` routes. It only returns measurements recorded for the **calling
tenant and actor**, so another user's session reads as `no_measurement`.
Statuses: `ok`, `no_measurement`, `unavailable`, `unknown_context_window`. It
returns `503` if migration `0006` has not been applied.

### Database migration (existing databases)

`infra/db/migrations/0006_context_guardian.sql` only adds a nullable
`models.context_window` column and the new `context_usage_measurements` table.
It is idempotent and deletes or changes no data. Fresh volumes get it
automatically. For an existing database, run from the repository root:

```sh
./scripts/backup-postgres.sh   # optional safety copy, written to tmp/
docker exec -i suez-postgres-1 sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  < infra/db/migrations/0006_context_guardian.sql
make deploy                    # rebuild and restart the stack with the new gateway
```

## Data location

Checkpoints are plain JSON under the OS app-data directory:

- macOS: `~/Library/Application Support/ai.acrosystems.suez.contextguardian/`
- Windows: `%APPDATA%\ai.acrosystems.suez.contextguardian\`
- Linux: `~/.local/share/ai.acrosystems.suez.contextguardian/`

```
checkpoints/<id>.json                    one file per checkpoint (schemaVersion 1 or 2)
checkpoints/.backups/<id>.v1.<ms>.json   original bytes of a v1 file, kept when it is first saved as v2
projects/<projectId>/memory.json         current project memory
projects/<projectId>/revisions/<n>.json  previous memory revisions (latest 50)
projects/<projectId>/memory.corrupt-<ms>.json   unreadable memory, kept after recovery
settings.json                            thresholds, usage source, gateway URL, session id,
                                         poll interval, active project id (never the API key)
```

The exact path is shown in Settings. Set `CONTEXT_GUARDIAN_DATA_DIR` to use a
different directory. Unreadable checkpoint files are skipped, reported in the
history view and left on disk untouched.

**Checkpoint schema versions.** Version 1 files (MVP and gateway phase) load
unchanged: every version 2 field has a default. Files are never rewritten
automatically. A version 1 file becomes version 2 only when you save it again,
for example by editing it or assigning it to a project. Its exact original
bytes are first copied to `checkpoints/.backups/` and the copy is verified. To
undo an upgrade, copy the backup back over `checkpoints/<id>.json`. A
checkpoint with an unsupported (future) version, or an unreadable one, is
listed as skipped and is never overwritten. Older builds of the app can still
list version 2 files (showing only the old fields) but refuse to save them.

## Prerequisites

- Node.js `^20.19` or `>=22.12` (developed with Node 25.2) and npm
- Rust stable via [rustup](https://rustup.rs) (developed with 1.99)
- macOS: Xcode or the Xcode Command Line Tools
- Windows / Linux: the [Tauri 2 system prerequisites](https://v2.tauri.app/start/prerequisites/)

## Commands

Run from `apps/context-guardian/`:

```sh
npm install                 # once
npm run tauri dev           # launch the native app with hot reload (first build ~5 min)

npm test                    # TypeScript unit tests (threshold, prompt, mock source, drafts)
npm run test:rust           # Rust storage tests
npm run typecheck           # tsc for app + vite config
npm run smoke               # native end-to-end check, mock mode (see below)
npm run test-stack -- up    # throwaway SUEZ stack for gateway tests (cg-test-* containers)
npm run smoke:gateway       # native end-to-end check against the real gateway (stub model)
npm run test-stack -- down  # remove the throwaway stack (add --purge for image + Go cache)

npm run tauri build         # release .app + .dmg in src-tauri/target/release/bundle/
```

### Native smoke test

`npm run smoke` builds the debug app and launches the **real native window**
twice against a throwaway temp directory (your checkpoints are never touched):

1. The app drives the mock usage to 50% → 84% → 86% → 96%, checks that exactly
   one draft is auto-saved and that the critical banner appears, edits fields,
   waits for autosave, marks the checkpoint final, copies the prompt, checks the
   history, starts a new session, checks that fields carry forward, and checks
   that the window resizes for each view.
2. The script checks the native window flags (always-on-top, frameless,
   visible) and, on macOS, that the system clipboard contains the prompt. Your
   previous clipboard text is restored afterwards.
3. The app is relaunched to check that both checkpoints persisted and render in
   history.

Options: `--skip-build` (reuse the last debug build), `--keep` (keep the temp
data dir), `--release` (test the release binary from `npm run tauri build`
instead of a debug build). The window appears briefly while it runs and then
closes itself.

### Gateway smoke test

`npm run smoke:gateway` needs the throwaway stack (`npm run test-stack -- up`).
That stack runs fresh Postgres/Redis/OPA/control-plane containers and the
gateway built from `apps/gateway`, as `cg-test-*` containers on
`127.0.0.1:18080`. It never touches the `suez-*` containers or their data,
and it only uses the free stub model.

The app runs with a throwaway data dir and Keychain service name. A driver
sends real chat requests with the app's session id. The app checks: not
configured → wrong key (401) → correct key (Keychain) → no measurement →
50% estimate → 85% draft (exactly one) → cache hit, no duplicate → 95%
critical → gateway stopped (STALE, no mock fallback) → gateway restarted →
prompt describes the gateway estimate. The driver then confirms that the key
is in no file on disk and is in the Keychain. It relaunches the app in the same
gateway session, still above 85%, and checks that no duplicate draft appears.
Finally it deletes the test Keychain item and restores your clipboard.

### Project-memory smoke test

`npm run smoke:memory` launches the real app three times against one throwaway
data dir seeded with a copy of the synthetic schema-1 checkpoint
`src-tauri/src/testdata/checkpoint-v1-sample.json`.
It uses a throwaway Keychain service. The run covers:

- import preview, then approve; historical "VERIFIED" lines stay `reported`;
- the checkpoint is unchanged by the import;
- project drafts are built from memory;
- `verified` is blocked without evidence (UI and Rust);
- a test result with provenance;
- merge preview, then approve;
- stale-revision and item-removal refusals;
- an approval change recorded only;
- the secret guard;
- the memory-aware prompt on the clipboard;
- explicit assignment with a byte-identical backup;
- restart persistence and restoring a revision;
- recovery from a corrupted `memory.json`, which is quarantined, not deleted.

### Release build

`npm run tauri build` (about 10 minutes from a clean state) produces:

- `src-tauri/target/release/bundle/macos/SUEZ Context Guardian.app`
- `src-tauri/target/release/bundle/dmg/SUEZ Context Guardian_0.1.0_aarch64.dmg`

The app has only an ad-hoc signature. It runs on the Mac that built it, but
Gatekeeper will block it on other Macs until it is signed and notarized with an
Apple Developer ID (Apple Developer Program, $99/year). The build is
Apple Silicon only (`aarch64`).

**Not automated:** dragging the window by its title bar (synthetic mouse input
needs macOS Accessibility permission). Check it by hand: `npm run tauri dev`,
then drag the title bar.

## Project layout

```
src/
  App.tsx                     state, source switching, guard wiring, views, window sizing
  api.ts                      Tauri command / window / clipboard wrappers
  types.ts                    Checkpoint, UsageSnapshot, settings
  guardian/threshold.ts       pure trigger state machine (+ tests)
  guardian/checkpoint.ts      draft creation, carry-forward with downgrade, restart guard helper
                              (+ prompt.test.ts, carry.test.ts, restart.test.ts)
  guardian/memory.ts          project memory: legacy import, merge proposals, change preview (+ tests)
  guardian/prompt.ts          continuation prompt builder (+ prompt.test.ts, prompt-memory.test.ts)
  usage/source.ts             UsageSource interface shared by both sources
  usage/mockSource.ts         simulated usage source (+ tests)
  usage/gatewayReading.ts     gateway result -> reading; what counts as trustworthy (+ tests)
  usage/gatewaySource.ts      polling gateway source (+ tests in gateway.test.ts)
  components/                 Monitor, CheckpointEditor, StructuredEditor, HistoryList,
                              ProjectPanel, SettingsPanel
  smoke.ts, smokeMemory.ts    in-app self-checks used only by the smoke scripts
src-tauri/
  src/lib.rs                  Tauri commands, data dir, Keychain service, smoke hooks
  src/storage.rs              JSON file store, checkpoint schema 1/2, evidence rules (+ tests)
  src/memory.rs               project memory store: revisions, conflicts, recovery (+ tests)
  src/testdata/               copy of a real schema-1 checkpoint used by regression tests
  src/gateway.rs              gateway HTTP client + Keychain storage (+ tests)
  tauri.conf.json             window: 360×236, frameless, always on top
  capabilities/default.json   minimal permissions (drag, resize, close, clipboard write)
scripts/
  smoke.mjs                   native end-to-end runner (mock)
  smoke-gateway.mjs           native end-to-end runner (real gateway, stub model, restart)
  smoke-memory.mjs            native end-to-end runner (project memory)
  gateway-test-stack.sh       throwaway cg-test-* SUEZ stack
```

## Limitations / roadmap

- Only requests sent through the SUEZ gateway with the session header are measured.
- Context windows are set per model in SQL; the control plane's model API
  does not expose `context_window` yet.
- Measurement rows are kept indefinitely; no retention or pruning yet.
- The stub model's counts are estimates. Provider-reported counts need a real
  provider, which costs money, so it has not been exercised in tests.
- Keychain storage targets macOS (Windows Credential Manager is compiled in
  but untested; Linux has no persistent backend configured).
- Optional server-side checkpoint storage and AI-assisted summaries are not built.
- Project memory is local to this Mac. It is not synced, and it is filled in by
  you or pasted from an AI session; the app cannot read AI conversations or
  verify claims itself. "Verified" means evidence was *recorded*, not that the
  app reran anything.
- Checkpoint upgrade backups (`checkpoints/.backups/`) are kept indefinitely
  and restored by copying the file back; there is no UI for them yet.
