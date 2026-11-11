# 0006. Extractive checkpoints that need human approval

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

A checkpoint is carried into a new session and treated as truth there. If a
model summarised the conversation, it could invent decisions or mark unfinished
work as done, and the next session would build on that.

## Decision

- Threshold-triggered checkpoints are **extractive**. They copy and cite stored
  evidence records:
  - the objective;
  - the last state;
  - the last request;
  - tool outputs.
- Decisions, assumptions and risks are left for a person to fill in.
- Checkpoints start as `draft`. Only an `approved` checkpoint can be used for a
  continuation or injected into a chat request.
- Evidence (what really happened) is stored apart from generated text, and
  summaries keep links to their sources.

## Consequences

- What is carried forward can be traced back to recorded evidence, and has
  been approved by a person.
- Someone has to review each draft, which is extra work compared with a fully
  automatic summary.
- An LLM summariser can be added later as a suggestion inside the draft
  without changing the approval rule.

## Alternatives considered

- **Automatic LLM summaries used directly:** rejected because there is a risk
  of invented content and no review step.
