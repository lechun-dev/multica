"use client";

import type { MemberWithUser } from "@multica/core/types";
import { useT } from "../../i18n";

export function MemberHandoverStatus({ handover }: { handover: NonNullable<MemberWithUser["handover"]> }) {
  const { t } = useT("settings");
  const statuses = {
    pending: handover.reason ? t(($) => $.member_handover.automation.attention) : t(($) => $.member_handover.automation.pending),
    failed: t(($) => $.member_handover.automation.failed),
    completed: t(($) => $.member_handover.automation.completed),
    no_work: t(($) => $.member_handover.automation.no_work),
    unknown: t(($) => $.member_handover.automation.unknown),
  };
  const reasons: Record<string, string> = {
    manager_read_failed: t(($) => $.member_handover.automation.manager_read_failed),
    manager_not_synced: t(($) => $.member_handover.automation.manager_not_synced),
    manager_unset: t(($) => $.member_handover.automation.manager_unset),
    manager_unlinked: t(($) => $.member_handover.automation.manager_unlinked),
    manager_inactive: t(($) => $.member_handover.automation.manager_inactive),
    manager_ready: t(($) => $.member_handover.automation.people_unavailable),
    people_unavailable: t(($) => $.member_handover.automation.people_unavailable),
    source_not_manageable: t(($) => $.member_handover.automation.source_not_manageable),
    execution_failed: t(($) => $.member_handover.automation.execution_failed),
  };
  return (
    <div className="text-caption text-muted-foreground break-words" role="status">
      <span>{statuses[handover.status] ?? statuses.unknown}</span>
      {handover.recipient_name && <span> · {handover.recipient_name}</span>}
      {handover.reason && <span> · {reasons[handover.reason] ?? t(($) => $.member_handover.automation.people_unavailable)}</span>}
      {handover.notification_status === "pending" && <span> · {t(($) => $.member_handover.automation.notification_pending)}</span>}
      {handover.notification_status === "delivered" && <span> · {t(($) => $.member_handover.automation.notification_delivered)}</span>}
      {handover.notification_status === "failed" && <span> · {t(($) => $.member_handover.automation.notification_failed)}</span>}
      {handover.notification_status === "staged" && <span> · {t(($) => $.member_handover.automation.notification_staged)}</span>}
    </div>
  );
}
