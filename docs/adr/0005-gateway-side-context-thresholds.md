# 0005. Context thresholds enforced in the gateway

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

Context Guardian first measured context usage and applied its thresholds on
the desktop. That only helped people running the app, and the thresholds
could differ between machines. The gateway already sees every request and its
token counts.

## Decision

- The gateway works out context usage for each session (`X-SUEZ-Session-Id`)
  and compares it with the thresholds for the tenant and model:
  - notice at 80;
  - checkpoint at 85;
  - warning at 95;
  - block at 100;
  - the hard block is optional and off by default.
- It reports the result in `X-SUEZ-Context-*` response headers and on
  `GET /v1/context/usage`.
- When usage crosses the checkpoint threshold, it asks SUEZ Memory to create a
  checkpoint **draft**.
- Context Guardian reads the tenant's thresholds from the gateway rather than
  keeping its own.

## Consequences

- Any client gets consistent behaviour, with or without the desktop app.
- Percentages need a configured context window per model. Without one, the
  status is `unknown` and no threshold applies.
- Only traffic routed through the gateway is measured. The design cannot see
  sessions in other tools.

## Alternatives considered

- **Client-side only:** inconsistent, and it cannot enforce a block.
- **A tokenizer for every provider in the gateway:** heavy. Provider-reported
  usage is used, with a labelled conservative estimate as the fallback.
