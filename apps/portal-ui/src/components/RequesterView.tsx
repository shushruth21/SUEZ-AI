import { FormEvent, useState } from "react";

import {
  AccessGrant,
  AccessRequest,
  IdentityContext,
  ModelRecord,
  ToolRecord
} from "../types";

function extractChatResponseText(chatResult: string) {
  if (!chatResult.trim()) {
    return "";
  }

  try {
    const parsed = JSON.parse(chatResult) as {
      error?: string;
      choices?: Array<{
        message?: {
          content?: string | Array<{ type?: string; text?: string }>;
        };
      }>;
    };
    const content = parsed.choices?.[0]?.message?.content;
    if (typeof content === "string") {
      return content;
    }
    if (Array.isArray(content)) {
      return content
        .map((item) => (typeof item?.text === "string" ? item.text : ""))
        .filter(Boolean)
        .join("\n");
    }
    return parsed.error ?? chatResult;
  } catch {
    return chatResult;
  }
}

type RequesterViewProps = {
  identity: IdentityContext | null;
  models: ModelRecord[];
  tools: ToolRecord[];
  accessRequests: AccessRequest[];
  accessGrants: AccessGrant[];
  modelRequestForm: { resourceName: string; justification: string };
  toolRequestForm: { resourceName: string; justification: string };
  chatForm: { model: string; prompt: string; maxTokens: string };
  toolRunForm: { name: string; input: string };
  chatResult: string;
  toolResult: string;
  onModelRequestFieldChange: (field: "resourceName" | "justification", value: string) => void;
  onToolRequestFieldChange: (field: "resourceName" | "justification", value: string) => void;
  onChatFieldChange: (field: "model" | "prompt" | "maxTokens", value: string) => void;
  onToolRunFieldChange: (field: "name" | "input", value: string) => void;
  onSubmitModelRequest: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onSubmitToolRequest: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onInvokeChat: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onExecuteTool: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
};

export function RequesterView({
  identity,
  models,
  tools,
  accessRequests,
  accessGrants,
  modelRequestForm,
  toolRequestForm,
  chatForm,
  toolRunForm,
  chatResult,
  toolResult,
  onModelRequestFieldChange,
  onToolRequestFieldChange,
  onChatFieldChange,
  onToolRunFieldChange,
  onSubmitModelRequest,
  onSubmitToolRequest,
  onInvokeChat,
  onExecuteTool
}: RequesterViewProps) {
  const requesterGrants = accessGrants.filter((grant) => grant.subject === identity?.actor);
  const chatResponseText = extractChatResponseText(chatResult);
  const [activeTab, setActiveTab] = useState<
    "identity" | "requests" | "grants" | "models" | "tools"
  >("identity");

  return (
    <section className="panel panel-modern">
      <div className="section-tabs" aria-label="Requester sections">
        <button
          type="button"
          className={activeTab === "identity" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("identity")}
        >
          Identity
        </button>
        <button
          type="button"
          className={activeTab === "requests" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("requests")}
        >
          Requests
        </button>
        <button
          type="button"
          className={activeTab === "grants" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("grants")}
        >
          Grants
        </button>
        <button
          type="button"
          className={activeTab === "models" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("models")}
        >
          Models
        </button>
        <button
          type="button"
          className={activeTab === "tools" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("tools")}
        >
          Tools
        </button>
      </div>

      <div className="tab-content">
        {activeTab === "identity" ? (
          <section className="subpanel">
            <h2>Identity Context</h2>
            {identity ? (
              <pre className="code-block">{JSON.stringify(identity, null, 2)}</pre>
            ) : null}
          </section>
        ) : null}

        {activeTab === "requests" ? (
          <section className="subpanel">
            <h2>Catalog-Based Access Requests</h2>
            <div className="split">
              <form className="form" onSubmit={onSubmitModelRequest}>
                <h3>Model Access</h3>
                <label>
                  Model
                  <select
                    value={modelRequestForm.resourceName}
                    onChange={(event) =>
                      onModelRequestFieldChange("resourceName", event.target.value)
                    }
                  >
                    {models.map((model) => (
                      <option key={model.id} value={model.externalName}>
                        {model.externalName}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Justification
                  <textarea
                    value={modelRequestForm.justification}
                    onChange={(event) =>
                      onModelRequestFieldChange("justification", event.target.value)
                    }
                  />
                </label>
                <button type="submit" disabled={!identity || models.length === 0}>
                  Request model access
                </button>
              </form>

              <form className="form" onSubmit={onSubmitToolRequest}>
                <h3>Tool Access</h3>
                <label>
                  Tool
                  <select
                    value={toolRequestForm.resourceName}
                    onChange={(event) =>
                      onToolRequestFieldChange("resourceName", event.target.value)
                    }
                  >
                    {tools.map((tool) => (
                      <option key={tool.id} value={tool.name}>
                        {tool.name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Justification
                  <textarea
                    value={toolRequestForm.justification}
                    onChange={(event) =>
                      onToolRequestFieldChange("justification", event.target.value)
                    }
                  />
                </label>
                <button type="submit" disabled={!identity || tools.length === 0}>
                  Request tool access
                </button>
              </form>
            </div>
            <div className="table-shell">
              <table>
                <thead>
                  <tr>
                    <th>Subject</th>
                    <th>Resource</th>
                    <th>Status</th>
                    <th>Created</th>
                  </tr>
                </thead>
                <tbody>
                  {accessRequests.map((request) => (
                    <tr key={request.id}>
                      <td>{request.subject}</td>
                      <td>{`${request.resourceType}:${request.resourceName}`}</td>
                      <td>{request.status}</td>
                      <td>{new Date(request.createdAt).toLocaleString()}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        ) : null}

        {activeTab === "grants" ? (
          <section className="subpanel">
            <h2>Granted Resources</h2>
            <div className="table-shell">
              <table>
                <thead>
                  <tr>
                    <th>Subject</th>
                    <th>Resource</th>
                    <th>Granted by</th>
                    <th>Status</th>
                    <th>Created</th>
                  </tr>
                </thead>
                <tbody>
                  {requesterGrants.map((grant) => (
                    <tr key={grant.id}>
                      <td>{grant.subject}</td>
                      <td>{`${grant.resourceType}:${grant.resourceName}`}</td>
                      <td>{grant.grantedBy}</td>
                      <td>{grant.status}</td>
                      <td>{new Date(grant.createdAt).toLocaleString()}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        ) : null}

        {activeTab === "models" ? (
          <section className="subpanel">
            <h2>Invoke Approved Model</h2>
            <div className="split">
              <form className="form" onSubmit={onInvokeChat}>
                <label>
                  Model
                  <select
                    value={chatForm.model}
                    onChange={(event) => onChatFieldChange("model", event.target.value)}
                  >
                    {models.map((model) => (
                      <option key={model.id} value={model.externalName}>
                        {model.externalName}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Prompt
                  <textarea
                    value={chatForm.prompt}
                    onChange={(event) => onChatFieldChange("prompt", event.target.value)}
                  />
                </label>
                <label>
                  Max tokens
                  <input
                    value={chatForm.maxTokens}
                    onChange={(event) => onChatFieldChange("maxTokens", event.target.value)}
                  />
                </label>
                <button type="submit" disabled={!identity || models.length === 0}>
                  Run chat completion
                </button>
              </form>
              <div className="result-shell response-window">
                <div className="response-window-header">
                  <h3>Response</h3>
                  <span className="muted">{chatForm.model || "No model selected"}</span>
                </div>
                {chatResponseText ? (
                  <pre className="response-block">{chatResponseText}</pre>
                ) : (
                  <div className="response-placeholder">
                    Run a model to see the response here.
                  </div>
                )}
                {chatResult ? (
                  <details className="response-raw">
                    <summary>Raw payload</summary>
                    <pre className="code-block">{chatResult}</pre>
                  </details>
                ) : null}
              </div>
            </div>
          </section>
        ) : null}

        {activeTab === "tools" ? (
          <section className="subpanel">
            <h2>Execute Approved Tool</h2>
            <div className="split">
              <form className="form" onSubmit={onExecuteTool}>
                <label>
                  Tool
                  <select
                    value={toolRunForm.name}
                    onChange={(event) => onToolRunFieldChange("name", event.target.value)}
                  >
                    {tools.map((tool) => (
                      <option key={tool.id} value={tool.name}>
                        {tool.name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  JSON input
                  <textarea
                    value={toolRunForm.input}
                    onChange={(event) => onToolRunFieldChange("input", event.target.value)}
                  />
                </label>
                <button type="submit" disabled={!identity || tools.length === 0}>
                  Execute tool
                </button>
              </form>
              <div className="result-shell">
                <pre className="code-block">{toolResult}</pre>
              </div>
            </div>
          </section>
        ) : null}
      </div>
    </section>
  );
}
