# Policy LLM (Open Source)

Policy drafting uses an **open source LLM** via `ollama/ollama` as the local policy LLM runtime.

- The container is started in Compose so the rest of the stack has a stable dependency to target.
- A pinned local model is pulled on startup by the `policy-llm-init` Compose service.
- The default pinned model is the open source `qwen2.5-coder:1.5b`, configured through `POLICY_LLM_MODEL`. Other Ollama-compatible open source models can be used.
- Ollama model blobs are persisted in the `ollama-data` volume so subsequent starts do not re-download the model.
- The control-plane now treats a configured but missing policy model as an error instead of silently falling back to the template generator.
