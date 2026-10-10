# Local Secrets

Place optional file-backed secrets in this directory when you do not want them stored directly in `.env`.

Current file paths consumed by the local stack:

- `openai_compat_api_key`: used by the `gateway` as `OPENAI_COMPAT_API_KEY_FILE`
- `groq_api_key`: used by the `gateway` as `GROQ_API_KEY_FILE` (for Groq provider models)

The application still falls back to normal environment variables when these files are absent.
