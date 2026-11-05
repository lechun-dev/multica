"use client";

import { useRef, useState } from "react";
import { onlineManager, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api";
import { childIssuesOptions, issueDetailOptions, issueKeys } from "@multica/core/issues/queries";
import { labelKeys } from "@multica/core/labels";
import { Button } from "@multica/ui/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

export function IssueRefreshButton({ workspaceId, issueId, includeWorkspaceOwned }: {
  workspaceId: string;
  issueId: string;
  includeWorkspaceOwned: boolean;
}) {
  const { t } = useT("issues");
  const queryClient = useQueryClient();
  const buttonRef = useRef<HTMLButtonElement>(null);
  const [refreshing, setRefreshing] = useState(false);
  const label = t(($) => $.detail.refresh_task);
  const failure = t(($) => $.detail.refresh_task_failed);

  const refresh = async () => {
    if (refreshing) return;
    if (!onlineManager.isOnline()) {
      toast.error(failure);
      return;
    }

    setRefreshing(true);
    const queryKeys = [
      issueDetailOptions(workspaceId, issueId, includeWorkspaceOwned).queryKey,
      issueKeys.timeline(issueId),
      issueKeys.tasks(issueId),
      childIssuesOptions(workspaceId, issueId, includeWorkspaceOwned).queryKey,
      labelKeys.byIssue(workspaceId, issueId),
      issueKeys.attachments(issueId),
      issueKeys.subscribers(issueId),
      issueKeys.reactions(issueId),
    ];
    const [detail, ...related] = await Promise.allSettled(queryKeys.map((queryKey) =>
      queryClient.refetchQueries({ queryKey, exact: true, type: "active" }, {
        cancelRefetch: false,
        throwOnError: true,
      }),
    ));

    if (!buttonRef.current) return;
    setRefreshing(false);
    // The detail view already handles denied or removed tasks.
    if (detail?.status === "rejected" && detail.reason instanceof ApiError &&
        (detail.reason.status === 403 || detail.reason.status === 404)) return;
    if (detail?.status === "rejected" || related.some((result) => result.status === "rejected") ||
        queryKeys.some((key) => queryClient.getQueryState(key)?.fetchStatus === "paused")) {
      toast.error(failure);
    }
  };

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            ref={buttonRef}
            type="button"
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label={label}
            aria-busy={refreshing}
            disabled={refreshing}
            onClick={() => void refresh()}
          >
            <RefreshCw aria-hidden className={refreshing ? "animate-spin motion-reduce:animate-none" : undefined} />
          </Button>
        }
      />
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );
}
