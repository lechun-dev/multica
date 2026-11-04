"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type MemberHandoverPreview } from "@multica/core/api";
import type { MemberWithUser } from "@multica/core/types";
import { invalidateMemberHandover } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

export function MemberHandoverDialog({ workspaceId, member, members, onClose }: {
  workspaceId: string;
  member: MemberWithUser;
  members: MemberWithUser[];
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [recipient, setRecipient] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [expired, setExpired] = useState(false);
  const started = useRef(false);
  const preview = useMutation({
    mutationFn: async (recipientUserId?: string) => {
      const result = await api.previewMemberHandover(workspaceId, member.id, recipientUserId);
      if (result.source_user_id !== member.user_id) throw new Error("Unexpected handover source");
      return result;
    },
    retry: false,
  });
  const execute = useMutation({
    mutationFn: async (snapshot: MemberHandoverPreview) => {
      const result = await api.executeMemberHandover(workspaceId, member.id, snapshot.preview_id);
      if (result.source_user_id !== member.user_id || result.recipient_user_id !== snapshot.recipient_user_id) {
        throw new Error("Unexpected handover result");
      }
      return result;
    },
    retry: false,
    onSuccess: async () => {
      await invalidateMemberHandover(qc, workspaceId);
      onClose();
    },
    onError: () => {
      preview.reset();
      setError(t(($) => $.member_handover.execute_failed));
    },
  });
  const { mutate: loadPreview } = preview;
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    loadPreview(undefined);
  }, [loadPreview]);
  const snapshot = preview.data;
  useEffect(() => {
    setExpired(false);
    if (!snapshot?.expires_at) return;
    const remaining = Date.parse(snapshot.expires_at) - Date.now();
    if (remaining <= 0) { setExpired(true); return; }
    const timer = setTimeout(() => setExpired(true), remaining);
    return () => clearTimeout(timer);
  }, [snapshot]);
  const currentRecipient = recipient ?? snapshot?.recipient_user_id ?? "";
  const options = members.filter((m) => m.user_id !== member.user_id && m.account_disabled !== true)
    .map((m) => ({ value: m.user_id, label: m.name || m.email }));
  const busy = preview.isPending || execute.isPending;
  const canConfirm = !busy && !expired && !!snapshot?.preview_id && snapshot.recipient_user_id === currentRecipient &&
    options.some((option) => option.value === currentRecipient);
  const manager = snapshot?.manager_name
    ? t(($) => $.project_authorization_organizations.manager_label, { name: snapshot.manager_name })
    : snapshot?.manager_status === "unset"
      ? t(($) => $.project_authorization_organizations.manager_unset)
      : snapshot?.manager_status === "unlinked"
        ? t(($) => $.project_authorization_organizations.manager_unlinked)
        : t(($) => $.project_authorization_organizations.manager_not_synced);

  return <AlertDialog open onOpenChange={(open) => { if (!open && !busy) onClose(); }}>
    <AlertDialogContent className="flex max-h-[90dvh] flex-col sm:max-w-2xl">
      <AlertDialogHeader>
        <AlertDialogTitle>{t(($) => $.member_handover.title, { name: member.name || member.email })}</AlertDialogTitle>
        <AlertDialogDescription>{t(($) => $.member_handover.scope)}</AlertDialogDescription>
      </AlertDialogHeader>
      <div className="min-h-0 space-y-4 overflow-y-auto text-body">
        {snapshot && <div className="space-y-1 text-caption text-muted-foreground">
          <p>{manager}</p>
          {snapshot.manager_status === "inactive" && <p>{t(($) => $.project_authorization_organizations.manager_inactive)}</p>}
          {snapshot.manager_sync_status === "failed" && <p className="text-destructive">{t(($) => $.project_authorization_organizations.manager_failed)}</p>}
          {snapshot.manager_successful_at && <p>{t(($) => $.project_authorization_organizations.manager_last_synced, { time: new Date(snapshot.manager_successful_at).toLocaleString() })}</p>}
        </div>}
        <div className="space-y-2">
          <label id="handover-recipient-label">{t(($) => $.member_handover.recipient)}</label>
          <Select items={options} value={currentRecipient || null} disabled={busy} onValueChange={(value) => {
            setRecipient(value ?? "");
            preview.reset();
            setError("");
          }}>
            <SelectTrigger aria-labelledby="handover-recipient-label" className="w-full">
              <SelectValue placeholder={t(($) => $.member_handover.select_recipient)} />
            </SelectTrigger>
            <SelectContent>{options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}</SelectContent>
          </Select>
          {options.length === 0 && <p>{t(($) => $.member_handover.no_recipients)}</p>}
        </div>
        {preview.isPending && <p role="status">{t(($) => $.member_handover.loading)}</p>}
        {(preview.isError || error) && <p role="alert" className="text-destructive">{error || t(($) => $.member_handover.preview_failed)}</p>}
        {expired && <p role="alert" className="text-destructive">{t(($) => $.member_handover.expired)}</p>}
        {snapshot && <>
          {snapshot.projects.length + snapshot.issues.length === 0 && <p>{t(($) => $.member_handover.empty)}</p>}
          {([
            { items: snapshot.projects, label: t(($) => $.member_handover.projects, { count: snapshot.projects.length }) },
            { items: snapshot.issues, label: t(($) => $.member_handover.issues, { count: snapshot.issues.length }) },
          ]).map(({ items, label }) => <section key={label} className="space-y-2">
            <h3 className="font-medium">{label}</h3>
            <ul className="space-y-2">{items.map((item) => <li key={item.id} className="rounded-md border p-2">
              <p className="break-words">{item.title}</p>
              <p className="text-caption text-muted-foreground">{t(($) => $.member_handover.grant, { role: item.role })}</p>
            </li>)}</ul>
          </section>)}
          {!!snapshot.preview_id && <p className="text-caption text-muted-foreground">{t(($) => $.member_handover.consequence)}</p>}
        </>}
      </div>
      <AlertDialogFooter>
        <AlertDialogCancel disabled={busy}>{t(($) => $.members.confirm_cancel)}</AlertDialogCancel>
        <Button variant="outline" disabled={busy} onClick={() => {
          setRecipient(currentRecipient);
          setError("");
          loadPreview(currentRecipient || undefined);
        }}>{t(($) => $.member_handover.refresh)}</Button>
        <AlertDialogAction disabled={!canConfirm} aria-busy={execute.isPending} onClick={(event) => {
          event.preventDefault();
          if (canConfirm && snapshot) {
            setRecipient(currentRecipient);
            execute.mutate(snapshot);
          }
        }}>{execute.isPending ? t(($) => $.member_handover.submitting) : t(($) => $.member_handover.confirm)}</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>;
}
