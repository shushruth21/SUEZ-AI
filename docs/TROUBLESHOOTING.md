# Suez: Troubleshooting Guide

This document helps you diagnose and fix common issues with the Suez portal, stack, gateway, and demo tooling. Use it together with **docs/operations.md** (how to use the portal) and **docs/local-development.md** (setup and commands).

---

## 1. Portal and UI

### 1.1 Portal page does not load (blank or connection refused)

**Symptoms:** Browser shows "This site can't be reached," "Connection refused," or a blank page at the portal URL.

**Checks:**

1. **Stack running**  
   From repo root:
   ```bash
   make ps
   # or
   docker compose -f infra/compose/docker-compose.yml ps -a
   ```
   Confirm `portal-ui` is **Up** (and ideally **healthy**). If it is **Created** but not **Up**, see [Section 2](#2-stack-and-docker).

2. **Correct URL**  
   - Direct: **http://localhost:5173**  
   - Via edge: **https://localhost:5443** (or your configured edge host).  
   If you use the edge URL, ensure the edge service is running and that you accept the self-signed certificate (or have run the TLS bootstrap script).

3. **Port in use**  
   If something else is bound to 5173, the portal container may fail to start. Check logs:
   ```bash
   docker compose -f infra/compose/docker-compose.yml logs portal-ui --tail=50
   ```
   Change the host port if needed via `PORTAL_UI_PORT` in `.env` (see compose file for the mapping).

**Resolution:** Start or fix the stack (e.g. `make up` or `make deploy`), then reload the portal URL.

---

### 1.2 Sign-in fails ("Login failed" or generic error)

**Symptoms:** After clicking **Sign in**, the status message shows "Login failed." or another error; you remain on the landing page.

**Checks:**

1. **Credential**  
   - **Admin with API key:** Key must have been created in **Admin → Setup → API Key Generation** (or seeded). For the demo, use **Use demo admin key** so the default key is filled.  
   - **Requester with API key:** Use the exact key provided by an admin (no extra spaces; key is case-sensitive).  
   - **Requester with JWT:** JWT must be valid (signature, expiry, and issuer if enforced). For the demo, use **Use demo user1 JWT** (or user2) to fill the demo JWT.

2. **Control plane reachable**  
   The portal calls the control plane for `/api/v1/auth/session`. If the control plane is down or unreachable, login fails.  
   - In **Admin → Overview**, confirm **Control Plane** shows OK.  
   - From the host: `curl -s http://localhost:8081/healthz` should return 200.

3. **Auth header format**  
   - API key: `Authorization: ApiKey <your-key>`.  
   - JWT: `Authorization: Bearer <your-jwt>`.  
   The portal sets these from the **Credential** field based on **Auth** (API key vs JWT). Ensure you did not mix key and JWT with the wrong auth type.

4. **Response payload**  
   If the control plane returns 200 but without `identityContext`, the portal shows "Authentication response did not include identity context." That usually indicates a bug or misconfiguration in the control plane auth flow; check control plane logs.

**Resolution:** Fix or replace the credential, ensure control plane is up and returns identity, and try again. For a clean demo, run the seed and/or `demo-admin.sh` so the demo admin key exists.

---

### 1.3 Portal loads but data is missing (empty tables, no models)

**Symptoms:** You are signed in but Admin or Requester panels show empty lists (e.g. no models, no grants, no requests).

**Checks:**

1. **Identity**  
   In **Admin → Overview** (or **Requester → Identity**), confirm the **Identity Context** JSON looks correct (tenant, actor, roles). If it is empty or wrong, sign out and sign in again with a valid credential.

2. **Seed data**  
   After a fresh database (e.g. first `make up` or after `make clean`), migrations and seeds run. Seeds create demo models, tools, and sometimes grants. If you used `make clean`, re-run `make up` so seeds run again.

3. **API errors**  
   The portal fetches data from the control plane (e.g. `/api/v1/models`, `/api/v1/access-grants`). If those calls fail (network or 4xx/5xx), the UI may show empty state. Open the browser **Network** tab and look for failed requests to the control plane or gateway; check the response body and status. Ensure `VITE_CONTROL_PLANE_URL` and `VITE_GATEWAY_URL` used at build time point to URLs the browser can reach (e.g. if the portal is served from the edge, use the edge base URL so the browser does not call localhost:8081/8080 directly unless you intend that).

**Resolution:** Confirm identity and seed/state; fix any failing API calls (control plane up, CORS if applicable, correct env for portal build).

---

### 1.4 CORS or mixed content errors in the browser console

**Symptoms:** Console shows CORS policy errors or mixed content (e.g. HTTPS page calling HTTP API).

**Checks:**

1. **Portal and API origins**  
   If the portal is opened from `https://localhost:5443` but the portal was built with `VITE_CONTROL_PLANE_URL=http://localhost:8081`, the browser may block requests to a different origin or downgrade to HTTP. Build the portal with the same base URL users will use (e.g. edge URL) so API calls go to the same origin or an allowed one.

2. **Control plane / gateway CORS**  
   If you host the portal on a different host/port than the APIs, the control plane and gateway must allow the portal origin in CORS headers. Check their configuration and logs.

**Resolution:** Align portal env (e.g. `VITE_*`) with how you access the portal; configure CORS on the backend if the portal is on a different origin.

---

## 2. Stack and Docker

### 2.1 "make: docker: No such file or directory" or docker not found

**Symptoms:** `make build` or `make up` fails with docker not found.

**Cause:** The Docker CLI is not on the `PATH` used by Make (e.g. Docker installed under `$HOME/.docker/bin`).

**Resolution:**

- The repo **Makefile** prepends `$(HOME)/.docker/bin` to `PATH` and exports it. Ensure you are using that Makefile and that `$HOME/.docker/bin` contains `docker` and `docker compose` (or `docker-compose`).  
- Or run compose directly with an explicit path:
  ```bash
  export PATH="$HOME/.docker/bin:$PATH"
  docker compose -f infra/compose/docker-compose.yml up -d
  ```

---

### 2.2 Some containers stay in "Created" and never start

**Symptoms:** `docker compose ps -a` shows control-plane, gateway, portal-ui, or edge as **Created** (not **Up**). policy-llm-init may be **Up** for a long time and never exit.

**Cause:** Compose starts services in dependency order. **control-plane** depends on **policy-llm-init** with `condition: service_completed_successfully`. So policy-llm-init must **exit successfully** before control-plane (and thus gateway, portal-ui, edge) can start. If policy-llm-init never completes, the rest stay Created.

**Common reason policy-llm-init never completes:** It cannot resolve or reach the hostname **policy-llm** (e.g. DNS lookup for `policy-llm` fails inside the init container). That can happen if the policy-llm container was started in an earlier run and is no longer on the same Compose network, or was never registered with the DNS alias `policy-llm`.

**Checks:**

1. **policy-llm-init logs**  
   ```bash
   docker compose -f infra/compose/docker-compose.yml logs policy-llm-init
   ```
   If you see repeated errors about "lookup policy-llm ... no such host" or "could not connect to ollama," the init container cannot reach the policy-llm service.

2. **Network and alias**  
   The compose file should attach **policy-llm** to the `suez-net` network with an explicit alias:
   ```yaml
   networks:
     suez-net:
       aliases:
         - policy-llm
   ```
   So that other containers on the same network can resolve `policy-llm`. If this was added after some containers were already created, those containers might have been created without the alias; a full down/up fixes that.

**Resolution:**

1. **Full stack restart (recommended)**  
   Tear down and bring everything up again so all containers join the same network with correct DNS:
   ```bash
   docker compose -f infra/compose/docker-compose.yml down
   docker compose -f infra/compose/docker-compose.yml up -d
   ```
   Wait a few minutes for policy-llm-init to finish (it pulls the Ollama model on first run). Then confirm control-plane, gateway, portal-ui, and edge transition to **Up** (and healthy where applicable).

2. **One-off repair (if you cannot down)**  
   Manually connect the policy-llm container to the Compose network with the alias so policy-llm-init can resolve it, then restart policy-llm-init. Prefer full down/up for a clean state; see **docs/implementation.md** for the exact steps if needed.

---

### 2.3 control-plane or gateway exits with "lookup postgres ... no such host"

**Symptoms:** control-plane (or another service) exits soon after start. Logs show a error like "lookup postgres on 127.0.0.11:53: no such host" or "failed to connect to ... hostname resolving error."

**Cause:** The container cannot resolve another service’s hostname (e.g. **postgres**) on the Docker network. This usually happens when some containers were started in a previous Compose run and the network was recreated or containers were recreated, so they are no longer on the same network with consistent DNS.

**Resolution:** Perform a **full stack restart** so every service is attached to the current Compose network:
```bash
docker compose -f infra/compose/docker-compose.yml down
docker compose -f infra/compose/docker-compose.yml up -d
```
Re-check `docker compose ps -a` and logs until control-plane and gateway are Up and healthy.

---

### 2.4 policy-llm-init runs for a very long time (e.g. 30+ minutes)

**Symptoms:** policy-llm-init stays **Up** for tens of minutes; control-plane and the rest never start.

**Checks:**

1. **DNS**  
   As in [2.2](#22-some-containers-stay-in-created-and-never-start), confirm the init container can resolve `policy-llm`. Check logs for "no such host" or connection errors.

2. **Model download**  
   Once it can reach policy-llm, the init container runs `ollama pull` for the configured model (e.g. `qwen2.5-coder:1.5b`). The first pull can take several minutes depending on network and disk. Subsequent runs use the cached model and finish quickly.

**Resolution:** Fix DNS/network with a full down/up if needed. If DNS is fine, wait for the first pull to complete; later starts will be fast.

---

### 2.5 Need a completely clean state (database and volumes)

**Symptoms:** You want to reset the stack (e.g. after failed migrations or corrupted data).

**Resolution:**

```bash
docker compose -f infra/compose/docker-compose.yml down -v --remove-orphans
# or
make clean
```
Then:

```bash
make up
# or
make deploy
```
`-v` removes named volumes (e.g. postgres data, ollama data). On the next `up`, Postgres will re-run migrations and seeds from the mounted SQL files.

---

## 3. Gateway and API

### 3.1 "Run chat completion" fails (Requester → Models)

**Symptoms:** In Requester → Models, after clicking **Run chat completion**, the response panel shows an error (e.g. "Model invocation failed." or a message from the gateway).

**Checks:**

1. **Grant**  
   You must have an **approved grant** for the selected model. In **Requester → Grants**, confirm the model appears. If not, submit a **Request model access** in **Requests** and wait for admin approval.

2. **Gateway health**  
   In **Admin → Overview**, confirm **Gateway** is OK. From the host: `curl -s http://localhost:8080/healthz` should return 200.

3. **Model enabled and available**  
   The model must exist in the catalog and be enabled. Admin can check **Catalog → Model Registry**. If the model uses an upstream provider (e.g. OpenAI-compatible or Groq), the gateway must have the correct API key or config (e.g. file under `infra/secrets/` or env) or the upstream call may fail.

4. **Response body**  
   The portal displays the error message returned by the gateway or the control plane. Use that to distinguish auth errors (401/403), "model not found," "upstream error," etc.

**Resolution:** Ensure you have a grant, gateway is up, and the model is correctly configured; fix any upstream keys or URLs as needed.

---

### 3.2 "Execute tool" fails (Requester → Tools)

**Symptoms:** After clicking **Execute tool**, the result area shows an error.

**Checks:**

1. **Grant**  
   You must have an approved grant for the selected tool. Check **Requester → Grants**.

2. **JSON input**  
   The **JSON input** must be valid JSON and match the tool’s input schema. Invalid JSON or wrong shape often returns a 400 or a clear error from the gateway.

3. **Gateway and tool config**  
   Gateway must be healthy. For HTTP tools, the target URL and timeouts must be correct; for stub tools, the gateway uses built-in stub behavior.

**Resolution:** Confirm grant and valid JSON; check gateway/tool configuration and gateway logs if the error is unclear.

---

### 3.3 Cache always MISS (demo-cache.sh or Observability)

**Symptoms:** Running the same prompt twice (in the portal or via `demo-cache.sh`) shows **X-Suez-Cache: MISS** both times; Observability shows no or few cache hits.

**Checks:**

1. **Redis**  
   The gateway must be built and configured with Redis for response caching. If Redis is not configured or not reachable, every request is a MISS.

2. **Cache TTL**  
   Response cache TTL is typically on the order of tens of seconds (e.g. 60). If you wait longer than that between the two identical requests, the second can be a MISS because the entry expired.

3. **Request identity**  
   Cache keys usually include model, prompt, and often identity/tenant. If the two requests differ (e.g. different API key or user), they may not share a cache entry.

**Resolution:** Ensure Redis is running and the gateway is configured to use it; run the two requests close together and with the same credential and prompt; re-check Observability after a few runs.

---

## 4. Admin and Requester workflows

### 4.1 No models (or tools) in the Requester dropdown

**Symptoms:** In **Requester → Models** (or **Tools**), the Model (or Tool) dropdown is empty or does not list the resource you expect.

**Cause:** The dropdown is populated from resources for which **you have an approved grant**. Pending or denied requests do not add options.

**Resolution:** Submit a **Request model access** (or tool access) in **Requester → Requests**, then have an admin **Approve** in **Admin → Access**. After approval, refresh or re-open **Models** (or **Tools**) and the new grant should appear in the dropdown.

---

### 4.2 Approval queue is empty (Admin → Access)

**Symptoms:** Admin → Access shows no pending requests.

**Cause:** Either no one has submitted a request, or all requests have already been approved/denied.

**Resolution:** As a requester, submit a model or tool request from **Requester → Requests**. Then, as admin, open **Admin → Access** again; the new request should appear in the Approval Queue until you Approve or Deny it.

---

### 4.3 Observability shows zeros for cache/chat metrics

**Symptoms:** Admin → Observability shows 0 for Cache entries, Chat requests, Cache hits, etc.

**Checks:**

1. **Gateway observability endpoint**  
   The portal calls the gateway at `/v1/observability` (or the configured path). If the gateway does not expose this or returns empty/zero, the UI will show zero.

2. **No traffic yet**  
   If no one has run chat completions since the gateway started, counts will be zero. Run a few chat completions from the Requester → Models tab and refresh Observability.

3. **Auth**  
   The observability request is made with the admin’s auth. If that request fails (e.g. 401), the UI might show zeros or no data. Check the browser Network tab for the observability request.

**Resolution:** Confirm the gateway supports `/v1/observability` and Redis/cache is configured; generate some chat traffic; ensure the admin credential is valid for that request.

---

## 5. Demo scripts

### 5.1 demo-cache.sh: both requests show X-Suez-Cache: MISS

**Symptoms:** `./scripts/demo-cache.sh` (or with a model name) prints MISS for both request 1 and request 2.

**Checks:**

1. **Admin grant**  
   The script uses the admin API key. The admin must have a grant for the model you pass (default `demo-model`). Run `demo-admin.sh` (or equivalent) so the admin has access to the model.

2. **Gateway and Redis**  
   As in [3.3](#33-cache-always-miss-demo-cachesh-or-observability), the gateway must have Redis and caching enabled.

3. **Same payload**  
   The script sends the same JSON body twice. If you modified the script or env (e.g. different prompt) between runs, both could still be MISS if the cache key includes prompt and the first request failed or was not cached.

**Resolution:** Ensure admin has a grant, Redis is up and configured, and the script runs against a healthy gateway; then run the script again.

---

### 5.2 Browser demo (Playwright) times out or "Element not found"

**Symptoms:** `./scripts/run-demo-cache-video.sh` or `./scripts/run-browser-demo.sh` fails with a timeout or "Element not found" (e.g. "Sign out" or a button).

**Checks:**

1. **Portal URL**  
   The demo uses `PORTAL_URL` (default `http://localhost:5173`). If your portal is on another port or the edge URL, set:
   ```bash
   PORTAL_URL=https://suez.acrosystemsinc.ai:5443 ./scripts/run-browser-demo.sh
   ```

2. **Portal and stack up**  
   The portal must be reachable. The default `demo.mjs` only opens the landing page and saves `scripts/browser-demo/screenshots/landing.png`. If you extend the script with more steps, update selectors when the UI changes.

3. **Headless vs visible**  
   Run with `HEADLESS=0` (default for `run-browser-demo.sh`) to see the browser.

**Resolution:** Use the correct `PORTAL_URL`; ensure the portal container or dev server is up; if you add flows to `demo.mjs`, align selectors with the current UI.

---

## 6. Logs and diagnostics

**Useful commands:**

- **All services:**  
  `docker compose -f infra/compose/docker-compose.yml logs -f`
- **One service (last 100 lines):**  
  `docker compose -f infra/compose/docker-compose.yml logs --tail=100 <service>`
- **Container status:**  
  `docker compose -f infra/compose/docker-compose.yml ps -a`
- **Control plane health:**  
  `curl -s http://localhost:8081/healthz`
- **Gateway health:**  
  `curl -s http://localhost:8080/healthz`
- **Bootstrap/status:**  
  `curl -s http://localhost:8081/api/v1/bootstrap/status`

Replace `localhost` and ports if you use the edge or custom env. For more on deployment and implementation details, see **docs/implementation.md** and **docs/local-development.md**.

---

## 7. SUEZ Context Guardian

Reference: [apps/context-guardian/README.md](../apps/context-guardian/README.md).

### 7.1 Monitor shows `NO DATA`

The gateway has no measurement for this session yet. Send at least one
`/v1/chat/completions` request with `X-SUEZ-Session-Id: <id>` (use **Copy
header** on the monitor). Requests without the header are not measured.

### 7.2 Tokens are shown but no percentage

The model has no `models.context_window`. Set it with SQL (see
[CONFIGURATION.md](CONFIGURATION.md#model-context-windows)).

### 7.3 `gateway_error 503` / "migration 0006"

The database does not have migration `0006`. Back up, then apply it by hand
([infra/db/README.md](../infra/db/README.md)) and rebuild the gateway.

### 7.4 `auth_failed`

The stored API key was rejected (401/403). Save the correct key in Settings and
use **Test connection**.

### 7.5 `STALE` or `network_error`

The gateway did not answer within 5 s or no successful poll happened within
three poll intervals. Check the gateway URL and `curl <url>/healthz`. The app
stays on the gateway source and does not fall back to mock data.

### 7.6 macOS asks for Keychain access after a rebuild

Expected: the app is ad-hoc signed, so each build has a new signature. Choose
**Always Allow**.

### 7.7 A checkpoint is listed as skipped

The file is unreadable or uses a newer schema version. The app never rewrites
it. Inspect the JSON in the app-data directory; a v1 original saved before an
upgrade is in `checkpoints/.backups/`.

### 7.8 "memory is at revision N but this update was based on revision M"

Project memory changed after the preview was created. Reload and review the
proposal again.

### 7.9 Project memory is unreadable

In the project panel choose **Recover from this revision…** and then **Confirm recovery**; the unreadable file
is kept as `memory.corrupt-<time>.json`.

### 7.10 `cargo test` fails with a missing `dist` directory

Run `npm run build` first — the Tauri context embeds the built frontend.

