# Issue comment thread concurrency

## Execution contract

One agent executes at most one run per issue at a time. A second root comment
that mentions the agent creates a separate conversation and waits if the agent
already has an active run on that issue. Replies in the same thread continue to
use the existing ordering, coalescing and deferred-turn rules.

Runs on different issues can execute concurrently and share the agent's
configured `max_concurrent_tasks`. With a capacity of six, the agent can execute
one run on each of six issues at once. A queued run blocked by another run on its
issue does not occupy a slot or stop the agent from claiming work on other
issues. Cancelling or completing a run releases its slot.

Session lookup, overflow recovery, missing-rollout detection, Codex/Hermes
conversation stores and managed-workdir reuse are scoped to the comment thread.
A new thread does not inherit another thread's provider session or workdir.
Explicitly retired provider session IDs remain retired across the issue so that
an invalid session cannot be resurrected through another thread.

## Conservative execution boundaries

- The same issue rule applies to all runtimes, including those advertising
  `issue-thread-concurrency-v1`, and to unscoped runs and scheduled wakeups.
  Wakeup session lookup uses the actual delivery comment thread.
- A run remains active while dispatched, running, or waiting for a local
  directory. The rule is per agent and issue; it does not change multi-agent
  collaboration.
- An explicit `local_directory` assignment using `in_place` retains the directory
  lock. Two runs writing the same original directory may therefore wait even
  when they belong to separate issues. `worktree` assignments use
  separate run worktrees, with a short lock for snapshot preparation.
- Existing direct-chat and quick-create admission fences are unchanged.

## Upgrade

Issue-level admission is enforced by the backend. It does not require an
execution daemon upgrade or a schema migration. Runs already admitted by the
previous version are not interrupted. New runs for the same agent and issue
wait until all of those active runs finish.

The existing thread-scoped session and workdir behavior is preserved. Workdir
markers from before thread-scoped reuse do not identify a comment thread and
cannot be reused for a threaded run.

## Verification and staging acceptance

Local fixture tests cover serial execution of separate top-level mentions on
the same issue, claiming another issue past a blocked queue entry, concurrent
claims at configured capacities of one, two and six, and reuse of a freed slot.
Thread-scoped session recovery, provider session-file mounts and managed
workdir reuse retain their existing tests. These tests use fixture runs and fake
provider files; they do not establish successful execution by real agents.

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
2. Upgrade the staging backend.
3. Read the target agent's actual capacity from both UI and API. The user's
   intended setting is six; resolve any mismatch before the capacity test.
4. In LC-3110, wait for the first top-level mention to start executing before
   posting a second top-level mention of the same agent. Verify that the second
   run stays queued until the first finishes and that an unrelated issue can
   start in a free slot while it waits.
5. Submit runs to seven different issues with the agent set to six. Use
   controlled execution durations and compare actual start and finish times
   to verify that six can overlap and the seventh starts after a slot is freed.
6. Record enqueue, wakeup, claim and start timestamps if any run waits while an
   eligible slot is free, so a delivery delay can be distinguished from the
   admission rules.
7. Cancel or fail one run and retry/rerun it. Verify unrelated runs continue,
   their outputs stay in their own threads, and continuation resumes only the
   corresponding thread's context.

Real-agent staging acceptance and production/staging isolation verification are
pending. Preserve run IDs, timing evidence and backend/daemon versions with
the acceptance result.
