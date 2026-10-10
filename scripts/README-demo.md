# Product demonstration scripts

These scripts set up and run a repeatable product demo for Suez.

## Prerequisites

- Docker Compose stack running (`docker compose -f infra/compose/docker-compose.yml up -d`)
- `curl` and `jq` on the host (for API scripts)
- Optional: `CONTROL_PLANE_URL`, `GATEWAY_URL` (defaults: `http://localhost:8081`, `http://localhost:8080`)
- Optional: `SUEZ_DEMO_ADMIN_KEY` (default: `suez-demo-admin-key`)

## Browser UI

Open the portal in your browser to see models, users, grants, and governance during the demo:

- **Direct (Vite):** http://localhost:5173
- **Via edge (if configured):** https://localhost:5443 or https://suez.acrosystemsinc.ai:5443

Click **Use demo admin key**, then **Sign in** as admin. Then use **Catalog** to see the model registry (for example **model-alpha** and **model-beta** after `demo-admin.sh`), **Access** for the approval queue and grants, and **Governance** for policies and audit.

### Automated browser demo (Playwright)

An open-source [Playwright](https://playwright.dev/) script runs the same flow in a real browser and saves screenshots.

**Prerequisites:** Node.js 18+

**Run from repo root:**

```bash
./scripts/run-browser-demo.sh
```

First run installs Playwright and Chromium. A landing-page screenshot is written to `scripts/browser-demo/screenshots/landing.png`.

**Options:**

- `PORTAL_URL=https://localhost:5443 ./scripts/run-browser-demo.sh` — use the edge URL.
- `HEADLESS=0 ./scripts/run-browser-demo.sh` — show the browser window instead of headless.

**Or run from the browser-demo folder:**

```bash
cd scripts/browser-demo && npm install && npm run demo
```

## Order of execution

1. **demo-reset.sh** — Wipe all models, policies, access requests, and grants so the demo starts from a clean state.
2. **demo-admin.sh** — As admin, create two models: `model-alpha` and `model-beta` (stub provider), and grant admin access to `model-alpha`.
3. **demo-users.sh** — Create two users (API keys): `demo-user-alpha` and `demo-user-beta`. Request and approve so each has exactly one model (`demo-user-alpha` → `model-alpha`, `demo-user-beta` → `model-beta`).
4. **demo-policy.sh** [optional rule] — Generate a policy from a natural language rule (default rule or pass one as first argument). Shows the generated Rego from the policy LLM.
5. **demo-cache.sh** [model] — Call the gateway chat completions twice with the same prompt; the second response is served from Redis cache (`X-Suez-Cache: HIT`). Default model: **`demo-model`** (seeded in the database). After **demo-admin.sh**, pass **`model-alpha`** (or another created model) as the first argument so the admin has a grant.
6. **demo-groq-with-browser.sh** — Runs the Playwright landing screenshot (same as `run-browser-demo.sh`). Create a Groq model in the portal or with **demo-requesters-openai-groq.sh** first; the gateway needs `GROQ_API_KEY` or `infra/secrets/groq_api_key` to call Groq.
7. **demo-requesters-openai-groq.sh** — Create two requesters (API keys): **requester-openai** (access to `demo-model` / OpenAI-compatible only) and **requester-groq** (access to `groq-llama-70b` only). Creates the Groq model if missing. Prints both API keys for use in the portal or gateway.
8. **run-demo-requesters-visible.sh** — Same Playwright smoke test as `run-browser-demo.sh` with a visible browser (`HEADLESS=0`). For multi-user requester flows, use **demo-requesters-openai-groq.sh** or the portal manually.

## Examples

```bash
# Full demo from clean state
./scripts/demo-reset.sh
./scripts/demo-admin.sh
./scripts/demo-users.sh
./scripts/demo-policy.sh
./scripts/demo-cache.sh

# Custom natural language rule
./scripts/demo-policy.sh "Deny tool execution for high-risk tools unless the user has the admin role."

# Cache demo with a specific model
./scripts/demo-cache.sh model-beta

# Create Groq model and run browser demo (Groq key in file-based secrets or .env)
./scripts/demo-groq-with-browser.sh

# Create two requesters: one with OpenAI access, one with Groq access (API only)
./scripts/demo-requesters-openai-groq.sh

# Visible browser (landing screenshot)
./scripts/run-demo-requesters-visible.sh

# Custom Groq model name and upstream
GROQ_MODEL_NAME=groq-llama-8b GROQ_UPSTREAM_MODEL=llama-3.1-8b-instant ./scripts/demo-groq-with-browser.sh
```

## Notes

- **demo-users.sh** creates new API keys each run; the raw keys are printed only in the command output. For repeat runs you may need to run **demo-reset.sh** and **demo-admin.sh** again so models exist; **demo-users.sh** will create new users/grants.
- **demo-cache.sh** uses the admin key and expects admin to have a grant for the chosen model (demo-admin.sh grants admin `model-alpha`).
- Response cache TTL in the gateway is 60 seconds; after that the same prompt will again yield a cache MISS.
