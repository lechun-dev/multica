"use client";

import { AlertTriangle } from "lucide-react";
import { useT } from "../i18n";

// 2026-08-28 coder(lq): Keep the no-project collaboration warning informational; creation remains allowed.
export function NoProjectCollaborationHint({
  visible,
}: {
  visible: boolean;
}) {
  const { t } = useT("modals");

  if (!visible) return null;

  return (
    <div
      role="status"
      aria-live="polite"
      className="mx-4 mb-1 flex shrink-0 items-center gap-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-caption text-amber-800 dark:text-amber-200"
    >
      <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
      <span>{t(($) => $.create_issue.no_project_collaboration_hint)}</span>
    </div>
  );
}
