#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

echo "==> Checking private migration guard"
bash scripts/check-private-migration-conflicts.test.sh

echo "==> Checking project authorization policy"
(
  cd server
  go test ./pkg/projectauth
)

echo "==> Checking task access, inbox, chat, and personal-message contracts"
handler_contracts='^(TestCreateIssuePromotesAssigneeAndMentionedMember|TestCreateCommentPromotesMentionedMember|TestCreateCommentPromotesMentionedMemberForProjectlessIssue|TestUpdateCommentPromotesMentionedMember|TestUpdateCommentPromotesMentionedMemberForProjectlessIssue|TestProjectlessIssueCreateAndTriggerPreviewAllowedWhenPermissionsEnabled|TestListInboxShowsDirectMentionOutsideProjectMembership|TestInboxListsShipCommentPreviewNotFullComment|TestCompleteTask_ChatNonEmptyOutputWritesMessage|TestCompleteTask_ChatCallbackIdempotent|TestDirectChat_ClaimKeepsQueuedTurnsPairedWithReplies|TestDingTalkPersonalMessageClaimLeaseAndWaitingState)$'
handler_output="$(mktemp "${TMPDIR:-/tmp}/multica-handler-contracts.XXXXXX")"
trap 'rm -f "$handler_output"' EXIT
(
  cd server
  go test ./internal/handler -run "$handler_contracts" -count=1 -v
) | tee "$handler_output"
if grep -q '^Skipping tests:.*database' "$handler_output"; then
  echo "Private handler contracts did not run because PostgreSQL is unavailable." >&2
  echo "Run this suite through 'make private-contracts' so the test database is prepared." >&2
  exit 1
fi

echo "==> Checking isolated DingTalk delivery extension"
(
  cd extensions/dingtalk-notify
  go test ./...
)

echo "==> Checking persisted chat rendering"
pnpm --filter @multica/core exec vitest run \
  chat/message-cache.test.ts \
  chat/use-task-messages.test.tsx
pnpm --filter @multica/views exec vitest run \
  chat/components/chat-message-list.test.tsx \
  chat/components/use-chat-controller.test.ts \
  chat/components/use-chat-controller.test.tsx

echo "==> Checking MissionOS desktop identity and update channels"
pnpm --filter @multica/desktop exec vitest run \
  src/shared/desktop-identity.test.ts \
  src/main/updater.test.ts \
  scripts/package.test.mjs

echo "Private feature contracts passed."
