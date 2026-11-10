# Third-party code and tooling boundaries

- **Claude-Mem.** SUEZ Memory borrows ideas only (automatic observations, searchable timelines, progressive retrieval, continuity between sessions). No Claude-Mem code is included and it is not a runtime dependency. If a developer-only importer is ever added it must be disabled by default, opt-in, labelled experimental, never upload local data automatically, and respect Claude-Mem's license and notices.
- **REA (reverse-engineering tooling).** Not used by SUEZ and not a dependency. It may only be used for authorized internal testing or compatibility research on software we own or are authorized to inspect.
- **Go and npm dependencies** are listed in each `go.mod` / `package.json`; CI runs `govulncheck` and `npm audit`.
