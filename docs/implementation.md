# Implementation Document

This document describes recent implementation work for the Suez portal UI, stack deployment, and demo tooling. It is intended for developers and maintainers who need to understand or extend these features.

---

## 1. Portal UI

### 1.1 Requester response window (Models tab)

**Problem:** Requestors had no clear place to see model output when running a chat completion; the result was effectively an unlabeled code block that could appear empty.

**Solution:** A dedicated response panel in the Requester **Models** tab.

**Implementation:**

- **Location:** `apps/portal-ui/src/components/RequesterView.tsx`
- **Behavior:**
  - A **Response** panel is always visible next to the “Invoke Approved Model” form.
  - **Placeholder:** Before any run, the panel shows: “Run a model to see the response here.”
  - **Success:** The assistant reply is shown as readable text. The gateway response is parsed to support:
    - `choices[0].message.content` as a string
    - `choices[0].message.content` as an array of `{ type, text }` (e.g. vision)
    - `error` or raw JSON if parsing fails
  - **Raw payload:** The full JSON is available under a collapsible **Raw payload** `<details>`.
- **Helper:** `extractChatResponseText(chatResult)` in `RequesterView.tsx` centralizes parsing and fallbacks.
- **Styles:** `apps/portal-ui/src/styles.css` — `.response-window`, `.response-window-header`, `.response-block`, `.response-placeholder`, `.response-raw` (compact, dark panel with header and optional details).

**Relevant files:**

- `apps/portal-ui/src/components/RequesterView.tsx` (response UI + `extractChatResponseText`)
- `apps/portal-ui/src/styles.css` (response-window block)

---

### 1.2 Modernized Admin and Requester panels

**Problem:** Admin and Requester panels felt heavy: large buttons, bulky spacing, and inconsistent density.

**Solution:** A shared “modern” panel design applied to all Admin and Requester tab content.

**Implementation:**

- **Scope:** All Admin tabs (Overview, Access, Catalog, Governance, Setup, Observability) and all Requester tabs (Identity, Requests, Grants, Models, Tools).
- **Mechanism:** A single class `panel-modern` is applied to the root `<section className="panel">` in both:
  - `AdminView.tsx`
  - `RequesterView.tsx`
- **CSS (under `.panel-modern` in `styles.css`):**
  - **Subpanels/cards:** Tighter padding, 8px radius, darker background (`#0d1117`), lighter border; section titles as small uppercase (`0.6875rem`, `#6e7681`).
  - **Forms:** Smaller gaps; labels `0.75rem`; inputs/selects/textareas with reduced padding (`0.35rem 0.5rem`), `0.8125rem` font, 6px radius; textareas with lower min-height (64px).
  - **Buttons:** Compact by default (e.g. `0.35rem 0.65rem` padding, `0.75rem` font, 6px radius) for form and `.actions` buttons.
  - **Tables:** 6px radius, smaller cell padding and font; uppercase header style.
  - **Overview cards, queue cards, code blocks, response window, filter grid:** Reduced padding/font and tighter gaps where appropriate.
- **Setup panel:** Kept as a separate, more structured layout:
  - **Markup:** `.setup-panel` > `.setup-card` sections with `.setup-form`, `.setup-row`, `.setup-table-wrap`, `.setup-generated`, `.setup-label`.
  - **Buttons:** Explicit `.btn-compact` on primary actions.
  - **Styles:** `.setup-panel`, `.setup-card`, `.setup-form`, `.setup-label`, `.setup-table-wrap`, `.btn-compact`, etc. in `styles.css`.

**Relevant files:**

- `apps/portal-ui/src/components/AdminView.tsx` — `panel panel-modern`; Setup uses `setup-panel` and `setup-card`.
- `apps/portal-ui/src/components/RequesterView.tsx` — `panel panel-modern`.
- `apps/portal-ui/src/styles.css` — `.panel-modern` block and `.setup-*` / `.btn-compact` block.

---

## 2. Build and deployment

### 2.1 Makefile and Docker path

**Problem:** `make build` and `make up` failed when the Docker CLI was not on the default `PATH` (e.g. when Docker is installed under `$HOME/.docker/bin`).

**Solution:**

- **Makefile** (`Makefile` at repo root):
  - Set `PATH := $(HOME)/.docker/bin:$(PATH)` and `export PATH` so all Make targets run with Docker on the path.
  - Added a **`deploy`** target: `make build` then `docker compose up -d` (detached).
- **Usage:**
  - `make build` — rebuild all Compose service images.
  - `make up` — foreground, with rebuild.
  - `make deploy` — build then bring stack up in the background.

**Relevant file:** `Makefile`.

---

### 2.2 Compose stack: policy-llm DNS and init

**Problem:** `policy-llm-init` could run indefinitely because it could not resolve the hostname `policy-llm`. Control-plane, gateway, portal-ui, and edge stayed in `Created` waiting for `policy-llm-init` to complete. Root cause: the `policy-llm` container was not on the same Compose network as `policy-llm-init` with the expected DNS name (e.g. after partial or older `docker compose up` runs).

**Solution:**

1. **Explicit network alias for policy-llm**  
   In `infra/compose/docker-compose.yml`, the `policy-llm` service is attached to `suez-net` with an explicit alias so the hostname `policy-llm` always resolves for other services on that network:

   ```yaml
   networks:
     suez-net:
       aliases:
         - policy-llm
   ```

2. **Recovery / clean state**  
   If the stack was left in a bad state (e.g. policy-llm not on the same network), a full restart fixes it:
   - `docker compose -f infra/compose/docker-compose.yml down`
   - `docker compose -f infra/compose/docker-compose.yml up -d`
   All services then attach to the same network with correct DNS.

**Relevant file:** `infra/compose/docker-compose.yml` (policy-llm `networks` with `aliases`).

---

## 3. Demo scripts and browser demo

### 3.1 Cache-hit CLI script (demo-cache.sh)

**Purpose:** Demonstrate response caching by sending the same chat request twice; the second response should be served from cache (`X-Suez-Cache: HIT`).

**Implementation:**

- **File:** `scripts/demo-cache.sh`
- **Behavior:**
  - Loads `.env` from repo root if present.
  - Uses `GATEWAY_URL` (default `http://localhost:8080`), `SUEZ_DEMO_ADMIN_KEY` (default `suez-demo-admin-key`), optional `DEMO_PROMPT` (default “What is 2+2? Reply in one word.”).
  - Model is first argument (default `demo-model`).
  - Builds JSON payload with the prompt escaped for JSON (no dependency on `jq` for the request body).
  - Sends two identical POSTs to `$GATEWAY_URL/v1/chat/completions`, prints `X-Suez-Cache` and a short response summary for each; uses `jq` for parsing when available, else a short fallback.
  - Prints a short note on success or if both requests were MISS.
- **Prerequisites:** Stack running; admin has a grant for the chosen model (e.g. after `demo-admin.sh`).

**Relevant file:** `scripts/demo-cache.sh`.

---

### 3.2 Browser demo (run-browser-demo.sh + demo.mjs)

**Purpose:** Smoke-test the portal in a real browser with Playwright and capture a screenshot for demos or CI-style checks.

**Implementation:**

- **Entry script:** `scripts/run-browser-demo.sh` — loads repo `.env` if present, sets `PORTAL_URL` (default `http://localhost:5173`) and `HEADLESS` (default `0` for a visible window), installs npm dependencies and Chromium on first run, then runs `npm run demo` in `scripts/browser-demo/`.

- **Playwright script:** `scripts/browser-demo/demo.mjs` — opens `PORTAL_URL`, waits for the page to settle, saves `scripts/browser-demo/screenshots/landing.png`.

- **Wrapper:** `scripts/run-demo-cache-video.sh` invokes `run-browser-demo.sh` (optional recordings path for future use). `scripts/demo-groq-with-browser.sh` and `scripts/run-demo-requesters-visible.sh` also delegate to `run-browser-demo.sh`.

**Relevant files:**

- `scripts/run-browser-demo.sh`
- `scripts/browser-demo/package.json`, `scripts/browser-demo/demo.mjs`
- `scripts/run-demo-cache-video.sh` (wrapper)

---

## 4. Updating and running the stack portal

To ship UI changes to the Compose-served portal:

1. **Build the UI:**  
   `cd apps/portal-ui && npm run build`

2. **Rebuild and update the portal container:**  
   From repo root (with Docker on `PATH`, e.g. via Makefile):  
   `docker compose -f infra/compose/docker-compose.yml build portal-ui`  
   `docker compose -f infra/compose/docker-compose.yml up -d portal-ui`

   Or one command:  
   `make build && docker compose -f infra/compose/docker-compose.yml up -d portal-ui`

3. **Access:**  
   Portal: **http://localhost:5173**  
   Edge (if running): **https://suez.acrosystemsinc.ai:5443** (with `127.0.0.1 suez.acrosystemsinc.ai` in `/etc/hosts`; see `docs/local-development.md`). **https://localhost:5443** works but may trigger a certificate warning.

For local development with hot reload, run `npm run dev` in `apps/portal-ui` (Vite may use a different port if 5173 is taken by the container).

---

## 5. File reference

| Area | Files |
|------|--------|
| Requester response window | `apps/portal-ui/src/components/RequesterView.tsx`, `apps/portal-ui/src/styles.css` (response-window) |
| Modern panels | `apps/portal-ui/src/components/AdminView.tsx`, `RequesterView.tsx`, `apps/portal-ui/src/styles.css` (panel-modern, setup-*) |
| Build/deploy | `Makefile` |
| Compose / policy-llm | `infra/compose/docker-compose.yml` |
| Cache CLI demo | `scripts/demo-cache.sh` |
| Browser demo (Playwright) | `scripts/run-browser-demo.sh`, `scripts/browser-demo/demo.mjs` |

---

## 6. Summary

- **Portal:** Requester Models tab has a clear response window (placeholder, parsed reply, raw payload). All Admin and Requester panels use a compact, modern style; Setup has a dedicated compact layout and compact buttons.
- **Deploy:** Makefile adds Docker to `PATH` and a `deploy` target; policy-llm has an explicit network alias so init and the rest of the stack can start reliably.
- **Demos:** Cache-hit CLI script is robust and documented; browser demo loads the portal and saves a landing screenshot (extend `demo.mjs` for fuller flows).
