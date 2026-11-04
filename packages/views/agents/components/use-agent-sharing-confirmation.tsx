"use client";

import { useEffect, useId, useRef, useState } from "react";
import { TriangleAlert } from "lucide-react";
import type { AgentInvocationTargetInput, MemberWithUser } from "@multica/core/types";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { useT } from "../../i18n";

interface SharingRequest {
  names: string[];
  targets: readonly AgentInvocationTargetInput[];
  members?: MemberWithUser[];
}

/** One acknowledgement per immutable save attempt, never a reusable permission. */
export function useAgentSharingConfirmation() {
  const { t } = useT("agents");
  const { t: tc } = useT("common");
  const id = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const [request, setRequest] = useState<SharingRequest | null>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const resolveRef = useRef<((allowed: boolean) => void) | null>(null);

  useEffect(() => () => { resolveRef.current?.(false); }, []);

  const finish = (allowed: boolean) => {
    const resolve = resolveRef.current;
    resolveRef.current = null;
    setRequest(null);
    setAcknowledged(false);
    resolve?.(allowed);
  };

  const confirmSharing = (next: SharingRequest): Promise<boolean> => {
    // A second attempt invalidates the first; approval cannot cross saves.
    resolveRef.current?.(false);
    setAcknowledged(false);
    setRequest(next);
    return new Promise((resolve) => { resolveRef.current = resolve; });
  };

  const audience = request?.targets.some((target) => target.target_type === "workspace")
    ? t(($) => $.sharing_warning.workspace)
    : request?.targets.map((target) =>
        request.members?.find((member) => member.user_id === target.target_id)?.name
          ?? target.target_id ?? t(($) => $.sharing_warning.selected_members),
      ).join(", ") ?? "";

  const sharingConfirmation = (
    <AlertDialog open={request !== null} onOpenChange={(open) => { if (!open) finish(false); }}>
      <AlertDialogContent initialFocus={cancelRef} className="max-h-[90dvh] overflow-y-auto data-[size=default]:sm:max-w-lg">
        <AlertDialogHeader>
          <TriangleAlert className="size-6 text-destructive" aria-hidden="true" />
          <AlertDialogTitle>{t(($) => $.sharing_warning.title)}</AlertDialogTitle>
          <AlertDialogDescription>{t(($) => $.sharing_warning.description)}</AlertDialogDescription>
        </AlertDialogHeader>
        <dl className="grid gap-2 text-body">
          <div><dt className="text-muted-foreground">{t(($) => $.sharing_warning.agents)}</dt><dd className="max-h-24 overflow-y-auto break-words">{request?.names.join(", ")}</dd></div>
          <div><dt className="text-muted-foreground">{t(($) => $.sharing_warning.audience)}</dt><dd className="max-h-24 overflow-y-auto break-words">{audience}</dd></div>
        </dl>
        <p className="text-body">{t(($) => $.sharing_warning.trust)}</p>
        <div className="flex items-start gap-3">
          <Checkbox id={id} checked={acknowledged} onCheckedChange={(checked) => setAcknowledged(checked === true)} />
          <label htmlFor={id} className="text-body">{t(($) => $.sharing_warning.acknowledgement)}</label>
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel ref={cancelRef}>{tc(($) => $.cancel)}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={!acknowledged} onClick={() => finish(true)}>
            {t(($) => $.sharing_warning.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
  return { confirmSharing, sharingConfirmation };
}
