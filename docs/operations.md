# Suez Portal: Operations Guide for Admins and Requesters

This guide explains how to use the Suez portal as an **Admin** or a **Requester**. It covers signing in, each section of the UI, and common workflows.

---

## 1. Accessing the portal

- **Direct (development / Compose):**  
  **http://localhost:5173**
- **Via HTTPS edge (when configured):**  
  **https://localhost:5443** or **https://suez.acrosystemsinc.ai:5443**

Ensure the Suez stack is running (e.g. `make up` or `make deploy` from the repo root). If the page does not load, check that the portal-ui service is up (`docker compose ps` or `make ps`).

---

## 2. Signing in

On the landing page you choose **who** you are (Admin or Requester), **how** you authenticate (API key or JWT), and then enter your credential.

### 2.1 Sign in as

- **Admin** — Full access: catalog, access approvals, governance, setup, observability.
- **Requester** — Request and use resources: request model/tool access, view grants, invoke models, run tools.

### 2.2 Requester user (when Sign in as = Requester)

- **user1** / **user2** — Select which requester identity you are using. This mainly matters when using **demo JWT** credentials; in production you would use your own API key or JWT per identity.

### 2.3 Auth

- **API key** — Enter an API key that has been generated in **Admin → Setup → API Key Generation** (or provided by your administrator). Used for both Admin and Requester when you have a key.
- **JWT** — Enter a JWT token. Typically used for requesters in environments where identity is issued as a JWT (e.g. from your IdP).

### 2.4 Credential

- Type or paste your **API key** or **JWT** in the Credential field.
- **Demo shortcuts:**
  - **Admin:** Click **Use demo admin key** to fill the default demo admin API key, then click **Sign in**.
  - **Requester:** Click **Use demo user1 JWT** (or user2) to fill the demo JWT for that user, then click **Sign in**.

After a successful sign-in you see a short status message and the main panel for your role (Admin or Requester). Use **Sign out** in the top bar to return to the landing page.

---

## 3. Admin guide

As an Admin you manage the catalog (models and tools), approve or deny access requests, govern policy, perform setup (tenants and API keys), and view observability.

### 3.1 Overview

- **Local Service Checks** — Status of Control Plane, Gateway, and Bootstrap API (e.g. reachable or error). Use this to confirm the stack is healthy.
- **Identity Context** — JSON showing the current authenticated identity (tenant, actor, roles, auth method). Useful to verify who you are signed in as.

### 3.2 Access

- **Approval Queue**  
  - **Decision note** — Optional note you can set before approving or denying; useful for audit.  
  - **Pending requests** — Each card shows the resource (e.g. `model:demo-model` or `tool:weather.lookup`), justification, and **Approve** / **Deny** buttons.  
  - Use **Approve** or **Deny** to resolve each pending request. Approved requesters receive a grant for that resource.

- **Grants View**  
  - Table of all access grants: Subject, Resource, Granted by, Status, Created.  
  - Use this to see who has access to which models and tools.

### 3.3 Catalog

- **Model Registry**  
  - **Form (left):** Create a new model: External name, Provider (stub, openai-compatible, groq), Stored model name, Upstream model, Upstream base URL override, Max RPM, Max TPM, Enabled.  
  - Click **Create model** to add the model. It appears in the table and can be requested by requesters.  
  - **Table (right):** Lists all models with External name, Provider, Model, Limits, Enabled.

- **Tool Registry**  
  - **Form (left):** Create a new tool: Tool name, Execution mode (stub, http), Description, Target URL, Timeout ms, Risk tier, Input schema, Output schema, Enabled.  
  - Click **Create tool** to add the tool.  
  - **Table (right):** Lists all tools with Name, Mode, Risk, Enabled.

### 3.4 Governance

- **Policy Registry**  
  - **Form (left):** Create or edit a policy: Name, Scope (runtime, model, tool), Target name, Natural language rule, Rego package, Rego source, Enabled.  
  - **Selected policy** shows the current draft or selected policy ID, status, version, and validation state.  
  - **Actions:** **Save draft**, **Generate draft** (from natural language), **Validate**, **Run tests**, **Publish**.  
  - **Table (right):** Lists policies with Name, Scope, Status, Version, Generator, Published, and row actions: **Edit**, **Validate**, **Test**, **Publish**.

- **Policy Tests**  
  - Add and run test cases for the selected policy (identity, resource, request, expected allow/reasons).  
  - **Add test case** and **Run selected tests** to validate policy behavior.

- **Version History**  
  - Table of policy versions for the selected policy: Version, Status, Published, Compiled, and actions **Inspect** / **Roll back**.

- **Audit View**  
  - **Filters:** Actor, Action, Resource type, Resource name, Decision, Trace ID, Since, Until, Limit. Click **Apply filters** to load events.  
  - **Table:** Audit events (When, Actor, Action, Resource, Decision, Trace).  
  - Click a **Trace** value to load **Trace detail** and see the full trace of events below.

### 3.5 Setup

- **Tenant Management**  
  - **Form:** Tenant name, Slug, Environment (dev, staging, prod). Click **Create tenant** to add a tenant.  
  - **Table:** All tenants (Name, Slug, Env).

- **API Key Generation**  
  - **Form:** Key label. Click **Generate key** to create an API key for the current tenant.  
  - **Raw API key** is shown once after generation; copy and store it securely. It cannot be retrieved again.  
  - **Table:** Existing API keys (Label, Tenant, Created).

### 3.6 Observability

- **Metrics** — Cards for:  
  - **Models** — Count of registered models.  
  - **Cache entries** — Chat response cache keys (e.g. in Redis).  
  - **Chat requests** — Total chat completion requests to the gateway.  
  - **Cache hits** — Requests served from cache.  
  - **Access requests** — Total access requests (model/tool grants).

Use this to monitor usage and cache effectiveness.

---

## 4. Requester guide

As a Requester you see your identity, submit access requests for models and tools, view your grants, and invoke approved models and tools.

### 4.1 Identity

- **Identity Context** — JSON for your current authenticated identity (tenant, actor, roles, auth method). Use this to confirm you are signed in as the correct user.

### 4.2 Requests

- **Catalog-Based Access Requests**  
  - **Model Access:** Choose a **Model** from the dropdown (models in the catalog), enter a **Justification**, and click **Request model access**. Your request appears in the table and goes to the Admin **Access** approval queue.  
  - **Tool Access:** Choose a **Tool**, enter a **Justification**, and click **Request tool access**. Same flow as model requests.  
  - **Table:** All your requests: Subject, Resource, Status, Created. Wait for an admin to **Approve** (or **Deny**) in the Admin portal; once approved, the resource appears in **Grants** and (for models) in **Models** for invocation.

### 4.3 Grants

- **Granted Resources** — Table of access grants for **your** identity: Subject, Resource, Granted by, Status, Created.  
  - Only resources listed here (and in the Models/Tools dropdowns) can be used in **Models** and **Tools** tabs.

### 4.4 Models

- **Invoke Approved Model**  
  - **Form (left):**  
    - **Model** — Dropdown of models you have been granted.  
    - **Prompt** — Your message to the model.  
    - **Max tokens** — Maximum tokens in the response.  
  - Click **Run chat completion** to send the request through the governed gateway.  
  - **Response (right):**  
    - Before any run: “Run a model to see the response here.”  
    - After a run: The model’s reply is shown as readable text.  
    - **Raw payload** — Expand to see the full JSON response (e.g. for debugging or inspection).  
  - If the request fails (e.g. no access, gateway error), the error message is shown in the response area.

Re-running the **same** prompt soon after may be served from cache (faster; see Admin **Observability** for cache hits).

### 4.5 Tools

- **Execute Approved Tool**  
  - **Form (left):**  
    - **Tool** — Dropdown of tools you have been granted.  
    - **JSON input** — Valid JSON input for the tool (must match the tool’s input schema).  
  - Click **Execute tool** to run the tool through the governed gateway.  
  - **Result (right):** The tool’s output is shown as JSON (or an error message if the call failed).

---

## 5. Common workflows

### 5.1 Admin: Add a model and grant a requester

1. Sign in as **Admin** (e.g. with the demo admin key).  
2. **Catalog → Model Registry:** Create the model (name, provider, limits, etc.) and click **Create model**.  
3. **Access:** When the requester’s request appears in the Approval Queue, add a decision note if desired and click **Approve**.  
4. The requester can then use **Requester → Models** to invoke that model.

### 5.2 Admin: Create an API key for a requester

1. Sign in as **Admin**.  
2. **Setup → API Key Generation:** Enter a **Key label** (e.g. the requester’s name or purpose) and click **Generate key**.  
3. Copy the **Raw API key** immediately; it is shown only once.  
4. Share the key and the portal URL (or gateway URL) with the requester.  
5. The requester signs in as **Requester** with **Auth** = API key and **Credential** = the key you provided.

### 5.3 Requester: Request access and use a model

1. Sign in as **Requester** (API key or JWT).  
2. **Requests:** In **Model Access**, select the model and enter a justification; click **Request model access**.  
3. Wait for an admin to approve (you can refresh **Grants** or **Models** to see when the grant appears).  
4. **Models:** Select the model, enter a prompt and max tokens, and click **Run chat completion**.  
5. Read the reply in the **Response** panel; expand **Raw payload** if you need the full JSON.

### 5.4 Requester: Run the same prompt twice (cache demo)

1. In **Models**, enter a prompt and click **Run chat completion** (first call is typically a cache **MISS**).  
2. Click **Run chat completion** again without changing the prompt (second call is often a cache **HIT**).  
3. An admin can confirm in **Observability** that **Cache hits** increased.

---

## 6. Tips and troubleshooting

- **Sign-in fails** — Check that your API key or JWT is valid and not revoked. For the demo admin key, ensure the stack is up and the control plane has created the demo tenant/key (e.g. after running seed or demo-admin script).  
- **No models in Requester dropdown** — You must have an approved **grant** for that model. Use **Requests** to request access and wait for admin approval; then check **Grants**.  
- **Run chat completion / Execute tool fails** — The response panel shows the error (e.g. “Model invocation failed” or a message from the gateway). Check grants, gateway health (Admin **Overview**), and (for tools) that your JSON input is valid.  
- **Approval queue empty** — Requesters must submit requests from **Requester → Requests**. If you are testing, sign in as a requester, submit a model or tool request, then sign back in as admin and open **Access**.  
- **Observability shows zeros** — Ensure the gateway and Redis are running. Cache and chat metrics come from the gateway’s observability endpoint.

---

## 7. Quick reference

| Role       | Main tasks |
|-----------|------------|
| **Admin** | Approve/deny requests (Access), create models and tools (Catalog), manage policies (Governance), create tenants and API keys (Setup), view metrics and audit (Overview, Observability, Audit View). |
| **Requester** | View identity (Identity), request model/tool access (Requests), view grants (Grants), run models (Models) and tools (Tools), and read responses in the Models response panel. |

| Auth       | Typical use |
|-----------|-------------|
| **API key** | Admin (e.g. demo admin key) or Requester when given an API key from Setup. |
| **JWT**     | Requester when your organization issues JWTs for the gateway. |

For deployment, build, and demo scripting, see **docs/implementation.md** and **scripts/README-demo.md**.
