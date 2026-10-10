# Installation

SUEZ is installed from source. There are **no published binaries or packages**.

## 1. Prerequisites

| Component | Requirement | Notes |
| --- | --- | --- |
| Gateway stack | Docker Desktop (or compatible) with Compose v2, GNU Make | ~several GB free for images and the Ollama model |
| Context Guardian | macOS on Apple Silicon | The only tested platform |
| | Node.js ≥ 20.19 | Vite 8 requirement; CI uses Node 22 |
| | Rust stable via [rustup](https://rustup.rs) | `rust-version = 1.77.2` minimum in `Cargo.toml` |
| | Xcode Command Line Tools | `xcode-select --install` |
| Go (optional) | Go 1.22 | Or run Go inside `golang:1.22-alpine` |

## 2. Get the source

```sh
git clone https://github.com/shushruth21/SUEZ-AI.git
cd SUEZ-AI
```

## 3. Gateway stack

```sh
cp .env.example .env     # optional: override ports or credentials
make up                  # build images and start everything (foreground)
# or: make deploy        # build and start in the background
```

On first start PostgreSQL runs every migration and seed in `infra/db/`, and
`policy-llm-init` pulls the Ollama model (this can take a while).

Optional HTTPS edge for the portal:

```sh
./scripts/bootstrap-local-tls.sh          # creates a local CA + cert, trusts the CA in your login keychain
echo "127.0.0.1 suez.acrosystemsinc.ai" | sudo tee -a /etc/hosts
```

See [local-development.md](local-development.md) for details and
[operations.md](operations.md) for using the portal.

Optional provider keys (paid upstream calls): set `OPENAI_COMPAT_API_KEY` /
`GROQ_API_KEY` in `.env`, or put them in `infra/secrets/openai_compat_api_key`
and `infra/secrets/groq_api_key`. Without them only the free **stub** model works.

### Existing databases

Migrations only run on a **new** volume. To add later migrations (e.g. `0006`)
to an existing database, back it up and apply the file by hand — see
[infra/db/README.md](../infra/db/README.md).

## 4. Context Guardian

```sh
cd apps/context-guardian
npm ci
npm run tauri dev          # development window with hot reload
```

To build a native app:

```sh
npm run tauri build
# → src-tauri/target/release/bundle/macos/SUEZ Context Guardian.app
# → src-tauri/target/release/bundle/dmg/SUEZ Context Guardian_0.1.0_aarch64.dmg
```

The build is ad-hoc signed and not notarized. To run it, open the `.app` from
Finder with **right-click → Open** the first time, or build it yourself.
See [RELEASE.md](RELEASE.md).

Then follow [Gateway mode setup](../apps/context-guardian/README.md#setup) to
connect it to your gateway.

## 5. Uninstall / clean up

| What | How |
| --- | --- |
| Stop the stack | `make down` |
| Stop and **delete the database volume** | `make clean` (irreversible — back up first) |
| Context Guardian data | Delete `~/Library/Application Support/ai.acrosystems.suez.contextguardian/` (your checkpoints and project memory — back up first) |
| Context Guardian API key | Settings → **Remove key**, or remove the Keychain item `ai.acrosystems.suez.contextguardian` / `gateway-api-key` |
| Throwaway test stack | `cd apps/context-guardian && npm run test-stack -- down --purge` |
