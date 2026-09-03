# Implementation Plan: Issue Archiving

## Overview

Add task archiving without changing the existing task status model. A nullable
`issue.archived_at` column marks archived tasks; backend queries exclude archived
tasks by default, while explicit filters can include them.

## Architecture Decisions

- Store archive state on `issue` to keep every existing query in the same row
  model and avoid omitted joins against a separate archive table.
- Keep the original status unchanged when archiving.
- Enforce archive and restore through the existing project permission module.
- Keep archived tasks readable and commentable, but require restore before
  mutations that advance or reassign a task.

## Task List

### Phase 1: Foundation

- [x] Add the additive migration and sqlc query support for archive state.
- [x] Add API response fields and archive/restore handlers with permission checks.

### Phase 2: Query and UI

- [x] Make task collections default to active tasks and support archive-state
      filtering.
- [x] Add board filtering plus archive and restore actions.

### Checkpoint: Complete

- [x] Focused backend tests pass (database-backed suites require PostgreSQL).
- [x] Frontend typecheck/tests pass for changed packages.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| A task query accidentally exposes archived work | Filter at SQL query boundaries and add regression tests. |
| Archive bypasses project permissions | Reuse the existing project authorization guard. |
| Upstream schema changes conflict | Use one additive nullable column and isolated handlers. |
