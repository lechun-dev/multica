import { useEffect } from "react";
import { useInboxUnreadCount } from "@multica/core/inbox/queries";
import { useWorkspaceTaskVisibility } from "../issues/surface/visibility-context";

type BadgeCapableAPI = {
  setUnreadBadge?: (count: number) => void;
};

function getDesktopAPI(): BadgeCapableAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: BadgeCapableAPI }).desktopAPI;
}

/**
 * Mirror the inbox unread count onto the OS dock/taskbar badge. No-op on web
 * (no `desktopAPI`) and on the login screen (no workspace ⇒ count defaults
 * to 0, which clears any stale badge from a previous session).
 */
export function useDesktopUnreadBadge(wsId: string | null | undefined): void {
  // 2026-09-01 coder(lq): Keep the OS badge on the same visibility scope as
  // the Inbox page so a workspace owner cannot infer hidden task activity.
  const { includeWorkspaceOwned, ready: visibilityReady } =
    useWorkspaceTaskVisibility();
  const count = useInboxUnreadCount(
    wsId,
    includeWorkspaceOwned,
    visibilityReady,
  );
  useEffect(() => {
    getDesktopAPI()?.setUnreadBadge?.(count);
  }, [count]);
}
