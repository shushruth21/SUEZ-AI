# Release Process

SUEZ has **no published releases** and no automated release pipeline. This
document describes how to prepare a build today and what is missing for a
proper public release.

## Versioning

- Context Guardian's version is set in three places that must match:
  `apps/context-guardian/package.json`, `src-tauri/Cargo.toml` and
  `src-tauri/tauri.conf.json` (currently `0.1.0`).
- Use [Semantic Versioning](https://semver.org). Record changes in
  [CHANGELOG.md](../CHANGELOG.md) under a new version heading.
- Tag releases as `context-guardian-vX.Y.Z` (desktop) or `vX.Y.Z` (stack) —
  no tags exist yet.

## Pre-release checklist

1. `main` is green in CI.
2. Run the local-only suites ([TESTING.md](TESTING.md)): native smoke tests
   (`smoke`, `smoke:memory`, `smoke:gateway`), the gateway integration test,
   and the ignored Keychain test.
3. Check dependencies: `npm audit` in each Node project; review `Cargo.lock`
   and `go.sum` changes.
4. Run a secret scan on the release commit (e.g. `gitleaks detect`).
5. Update `CHANGELOG.md`, versions and documentation (feature table,
   known limitations).
6. If the release includes a migration, document manual application for
   existing databases.

## Building Context Guardian

```sh
cd apps/context-guardian
npm ci
npm run tauri build
```

Artifacts (git-ignored, never commit them):

- `src-tauri/target/release/bundle/macos/SUEZ Context Guardian.app`
- `src-tauri/target/release/bundle/dmg/SUEZ Context Guardian_<version>_aarch64.dmg`

Verify the build:

```sh
hdiutil verify "src-tauri/target/release/bundle/dmg/SUEZ Context Guardian_<version>_aarch64.dmg"
node scripts/smoke.mjs --release
node scripts/smoke-memory.mjs --release
shasum -a 256 src-tauri/target/release/bundle/dmg/*.dmg   # publish alongside the artifact
```

## Signing and notarization (not set up)

Current builds are **ad-hoc signed** and **not notarized**:

- Gatekeeper blocks them on other Macs unless the user explicitly opens them.
- macOS may re-prompt for Keychain access after every rebuild because the code
  signature changes.

To distribute publicly you need an Apple Developer ID certificate, a
`signingIdentity` in `tauri.conf.json` (or `APPLE_SIGNING_IDENTITY`), and
notarization credentials supplied as CI secrets — see the Tauri macOS signing
guide. None of this is configured, and no credentials belong in the repository.

## Platform limitations

- Built and tested only for **macOS on Apple Silicon (aarch64)**. Intel/universal
  builds, Windows and Linux are untested.
- The Windows credential-store backend is compiled in but has never been run.

## Gateway stack

The stack is built from source with `make build`; images are not published to
any registry. There is no production deployment procedure in this repository.
Deployment to shared or cloud environments requires the hardening items in
[SECURITY-AND-PRIVACY.md](SECURITY-AND-PRIVACY.md) first.

## Distribution

Until signing and notarization exist, distribute source only. If you attach a
DMG to a GitHub release, label it as unsigned/experimental and publish its
SHA-256 checksum.
