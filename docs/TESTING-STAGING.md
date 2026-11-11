# Testing SUEZ on the shared staging server

You get two things from the SUEZ team:

- `ContextGuardian.dmg` — the desktop app;
- a text file with your **staging URL** and **two keys**:
  - an *admin* key, for the portal admin view;
  - a *desktop* key, for the Context Guardian app and the portal requester view.

Your keys only see your own sandbox (tenant `tester-<your name>`); nobody
else's data is visible to you, and yours is not visible to them. Keys expire
after 14 days. Everything runs on a free **stub** model: nothing you type is
sent to a real AI provider.

## 1. Portal (browser)

1. Open the staging URL.
2. **Admin view:** Sign in as **Admin**, Auth **API key**, paste the admin key. Try:
   - **Catalog:** your two models (`demo-model`, `demo-small`) and the `weather.lookup` tool.
   - **Setup → API key generation:** create a key with a scope, then **Revoke** it.
   - **Governance:** save a policy draft, **Validate**, add a test case, **Run tests**, **Publish**.
   - **Access:** approve or deny access requests.
   - **Observability:** usage counters and the audit log, including what your desktop app did.
3. **Requester view:** sign out, Sign in as **Requester**, Auth **API key**, paste the desktop key. Run a chat under **Models**.

## 2. Context Guardian (desktop app, macOS)

1. Open `ContextGuardian.dmg` and drag **Context Guardian** to **Applications**.
2. First launch only: the app is not signed yet, so **right-click it → Open → Open**.
3. Click **⚙ Settings**:
   - **Usage source:** gateway
   - **Gateway URL:** `<staging URL>/gateway`
   - **API key:** your desktop key
   Save, then **Test connection** — it should name your tenant.
4. Back on the monitor (◔), click **Copy session header**.

## 3. Make the meter move

Send chats with your session id (the command is in your text file with your
key filled in). `demo-small` has a 400-token window, so one long message
moves it a lot:

- below 80% → **ok**; 80% → **notice**; 85% → **checkpoint**; 95% → **warning**.
- At 85% SUEZ drafts a **checkpoint** automatically. It appears in the
  **SUEZ checkpoints** panel under the meter (click **Refresh**).

## 4. Checkpoints

In the panel:

1. **Review** the draft. It is labelled as automatically drafted and *not
   verified fact*; it cites the stored records it was built from.
2. **Approve** it (or **Reject** it).
3. **Continue in a new session**: the app switches to a fresh session and puts
   exactly what was carried forward on your clipboard. Nothing else from the
   old session is carried.

You can also **Upload latest** to send one of your own local checkpoints to
the server as a draft.

## Good to know

- The staging URL can change if the server restarts; you will get the new one.
- If something fails, note the time and the `X-SUEZ-Trace-Id` / `X-Request-Id`
  response header (or the message the app shows) and send it to the team.
- Things worth trying to break: using one key where it should not work (for
  example creating keys with the desktop key), very long messages, and
  continuing from a checkpoint that is not approved yet.

---

## For the SUEZ team: running staging

Staging runs on one Mac and is reached through a Cloudflare quick tunnel.

```bash
make dmg                                # build ./ContextGuardian.dmg
bash scripts/staging.sh up              # start; prints the public URL
bash scripts/staging.sh add-tester NAME # tenant + keys; writes ~/.suez-staging/testers/NAME.txt
bash scripts/staging.sh list            # testers and expiry
bash scripts/staging.sh revoke NAME     # revoke a tester's keys
bash scripts/staging.sh url             # current public URL
bash scripts/staging.sh down            # stop (data kept)
bash scripts/staging.sh destroy         # delete everything
```

- Production settings (`SUEZ_ENV=production`), no demo seeds and no demo key.
  Secrets are in `.env.staging` (git-ignored) and `~/.suez-staging`.
- The URL changes whenever the tunnel restarts. Tester files written earlier
  contain the old URL: run `url` and tell testers.
- A new quick-tunnel name can take about a minute to resolve.
- Staging is only up while this Mac is awake with Docker running. For an
  always-on staging server use `infra/compose/docker-compose.prod.yml`
  (see `docs/production/DEPLOYMENT.md`).
