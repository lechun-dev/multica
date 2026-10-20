"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { MemberWithUser } from "@multica/core/types";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

export type MemberStatusFilter = "all" | "enabled" | "disabled";

export function filterMembers(members: MemberWithUser[], search: string, status: MemberStatusFilter) {
  const needle = search.trim().toLocaleLowerCase();
  return members.filter((member) =>
    (status === "all" || (member.account_disabled === true) === (status === "disabled")) &&
    (!needle || member.name.toLocaleLowerCase().includes(needle) || member.email.toLocaleLowerCase().includes(needle)),
  );
}

export function MemberSearchControls({ search, status, onSearchChange, onStatusChange }: {
  search: string; status: MemberStatusFilter;
  onSearchChange: (value: string) => void; onStatusChange: (value: MemberStatusFilter) => void;
}) {
  const { t } = useT("settings");
  const options: { value: MemberStatusFilter; label: string }[] = [
    { value: "all", label: t(($) => $.members.status_all) },
    { value: "enabled", label: t(($) => $.members.status_enabled) },
    { value: "disabled", label: t(($) => $.members.status_disabled) },
  ];
  return <div className="flex flex-col gap-3 sm:flex-row">
    <Input type="search" value={search} onChange={(event) => onSearchChange(event.target.value)}
      aria-label={t(($) => $.members.search_placeholder)} placeholder={t(($) => $.members.search_placeholder)} />
    <Select items={options} value={status} onValueChange={(value) => {
      if (value === "all" || value === "enabled" || value === "disabled") onStatusChange(value);
    }}>
      <SelectTrigger className="sm:w-40" aria-label={t(($) => $.members.status_filter)}>
        <SelectValue>{() => options.find((option) => option.value === status)?.label}</SelectValue>
      </SelectTrigger>
      <SelectContent>{options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}</SelectContent>
    </Select>
  </div>;
}

export function MemberAccountAccessDialog({ workspaceId, member, onClose }: {
  workspaceId: string; member: MemberWithUser; onClose: () => void;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const disabling = member.account_disabled !== true;
  const submit = async () => {
    if (pending) return;
    setPending(true);
    setError("");
    try {
      await api.setMemberAccountAccess(workspaceId, member.id, disabling);
      await qc.invalidateQueries({ queryKey: workspaceKeys.members(workspaceId) });
      onClose();
    } catch {
      setError(t(($) => $.members.account_update_failed));
    } finally { setPending(false); }
  };
  return <AlertDialog open onOpenChange={(open) => { if (!open && !pending) onClose(); }}>
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{disabling ? t(($) => $.members.disable_title, { name: member.name || member.email }) : t(($) => $.members.enable_title, { name: member.name || member.email })}</AlertDialogTitle>
        <AlertDialogDescription>{disabling ? t(($) => $.members.disable_description) : t(($) => $.members.enable_description)}</AlertDialogDescription>
      </AlertDialogHeader>
      {error && <p role="alert" className="text-body text-destructive">{error}</p>}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={pending}>{t(($) => $.members.confirm_cancel)}</AlertDialogCancel>
        <AlertDialogAction disabled={pending} aria-busy={pending} variant={disabling ? "destructive" : "default"}
          onClick={(event) => { event.preventDefault(); void submit(); }}>
          {pending ? t(($) => $.members.account_updating) : disabling ? t(($) => $.members.disable_action) : t(($) => $.members.enable_action)}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>;
}
