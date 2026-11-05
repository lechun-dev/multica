# Issue comment thread concurrency

## Execution contract

One agent can execute independent root comment threads of one issue at the same
time, and can execute work from different issues at the same time. Replies in the
same thread continue to use the existing ordering, coalescing and deferred-turn
rules. Posting another root comment starts a separate conversation; replying to
an existing comment continues that conversation.

All work shares the agent's configured `max_concurrent_tasks`. There is no fixed
concurrency value of ten. For example, with a configured capacity of six, six
independent threads can be admitted across one or several issues; additional
work waits for capacity. Cancelling or completing one run releases only its slot.

Session lookup, overflow recovery, missing-rollout detection, Codex/Hermes
conversation stores and managed-workdir reuse are scoped to the comment thread.
A new thread does not inherit another thread's provider session or workdir.
Explicitly retired provider session IDs remain retired across the issue so that
an invalid session cannot be resurrected through another thread.

## Conservative execution boundaries

- A runtime must advertise `issue-thread-concurrency-v1` to admit parallel
  threads within one issue. Older runtimes retain issue-level serialization.
- Legacy or unscoped issue runs retain issue-level serialization. Scheduled
  wakeups also retain that fence; their scheduling IDs are not conversation IDs.
  Wakeup session lookup uses the actual delivery comment thread.
- An explicit `local_directory` assignment using `in_place` retains the directory
  lock. Two runs writing the same original directory may therefore wait even
  when they belong to separate threads or issues. `worktree` assignments use
  separate run worktrees, with a short lock for snapshot preparation.
- Existing direct-chat and quick-create admission fences are unchanged.

## Upgrade

Upgrade both the backend and the execution daemon. Upgrading only the backend
keeps an older daemon on the conservative issue fence. No schema migration is
required for this change.

Workdir markers written before this change do not identify a comment thread.
They cannot be reused for a threaded run. On the first continuation after
upgrade, the existing resume-presence checks may start a new provider session
and report unavailable previous context rather than sharing an unscoped workdir.

## Verification and staging acceptance

Local fixture tests cover independent-thread admission, same-thread ordering,
configured capacities of one, two and six, concurrent claims, slot replacement,
cancel/rerun isolation, thread-scoped session recovery, provider session-file
mounts, managed-workdir reuse and capability advertisement. These tests use an
isolated temporary PostgreSQL instance and fake provider files; they do not
establish successful execution by real agents.

The broader handler regression suite still fails
`TestClaimTask_IssueProjectInForeignWorkspace_CancelsTask`: it expects HTTP 409
but receives HTTP 200 with `task: null` after the invalid task is failed. The
same suite reproduces the identical failure on the unmodified `bc9297f3a`
baseline. This existing behavior is outside the concurrency change.

Before real-agent staging acceptance:

1. Confirm the staging URL, workspace, daemon profile, runtime registration and
   credentials select staging exclusively. A different URL alone is insufficient
   if production and staging see the same runtime or run IDs. Do not stop the
   production daemon to perform this check.
2. Upgrade the staging backend and daemon and verify the capability is registered.
3. Read the target agent's actual capacity from both UI and API. The user's
   intended setting is six; resolve any mismatch before the capacity test.
4. In LC-3110, start six independent root threads with distinguishable inputs and
   a controlled execution duration. Verify overlapping real run times/processes,
   isolated outputs and no more than six admitted executions.
5. Queue another root thread. Verify it waits and starts after one slot is freed.
   Reply in an active thread and verify that reply waits for its predecessor.
6. Repeat across multiple issues, checking the combined total remains six.
7. Cancel or fail one run and retry/rerun it. Verify unrelated runs continue,
   their outputs stay in their own threads, and continuation resumes only the
   corresponding thread's context.

Real-agent staging acceptance and production/staging isolation verification are
pending. Preserve run IDs, timing evidence and daemon version/capability details
with the acceptance result.
