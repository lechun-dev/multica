"use client";

import { useMemo, useState } from "react";
import { Pencil, Plus, RotateCcw, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useAuthStore } from "@multica/core/auth";
import {
  taskRetryPolicyListOptions,
  useCreateTaskRetryPolicy,
  useDeleteTaskRetryPolicy,
  useUpdateTaskRetryPolicy,
} from "@multica/core/task-retry-policies";
import type { TaskRetryPolicy, TaskRetryPolicyMatchType } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { useT } from "../../i18n";

type Draft = {
  name: string;
  enabled: boolean;
  priority: string;
  match_type: TaskRetryPolicyMatchType;
  match_value: string;
  max_attempts: string;
  delay_schedule: string;
};

const EMPTY_DRAFT: Draft = {
  name: "", enabled: true, priority: "100", match_type: "failure_reason",
  match_value: "", max_attempts: "2", delay_schedule: "0",
};

export function TaskRetryPoliciesTab() {
  const { t } = useT("settings");
  const workspaceId = useWorkspaceId();
  const currentUser = useAuthStore((state) => state.user);
  const { data: members = [] } = useQuery(memberListOptions(workspaceId));
  const { data: policies = [], isLoading, isError } = useQuery(taskRetryPolicyListOptions(workspaceId));
  const createPolicy = useCreateTaskRetryPolicy();
  const updatePolicy = useUpdateTaskRetryPolicy();
  const deletePolicy = useDeleteTaskRetryPolicy();
  const isOwner = useMemo(
    () => members.some((member) => member.user_id === currentUser?.id && member.role === "owner"),
    [members, currentUser?.id],
  );
  const [editing, setEditing] = useState<TaskRetryPolicy | null>(null);
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState<Draft>(EMPTY_DRAFT);

  const openCreate = () => { setDraft(EMPTY_DRAFT); setEditing(null); setCreating(true); };
  const openEdit = (policy: TaskRetryPolicy) => {
    setDraft({
      name: policy.name, enabled: policy.enabled, priority: String(policy.priority),
      match_type: policy.match_type, match_value: policy.match_value,
      max_attempts: String(policy.max_attempts), delay_schedule: policy.delay_schedule.join(", "),
    });
    setEditing(policy); setCreating(false);
  };
  const close = () => { if (!createPolicy.isPending && !updatePolicy.isPending) { setCreating(false); setEditing(null); } };
  const save = async () => {
    const delays = draft.delay_schedule.split(",").map((value) => Number(value.trim()));
    if (!draft.name.trim() || !draft.match_value.trim() || delays.some((value) => !Number.isInteger(value) || value < 0 || value > 86400)) {
      toast.error(t(($) => $.task_retry_policies.invalid)); return;
    }
    const payload = {
      name: draft.name.trim(), enabled: draft.enabled, priority: Number(draft.priority) || 0,
      match_type: draft.match_type, match_value: draft.match_value.trim(),
      max_attempts: Number(draft.max_attempts) || 1, delay_schedule: delays.length ? delays : [0],
    };
    try {
      if (editing) await updatePolicy.mutateAsync({ policyId: editing.id, ...payload });
      else await createPolicy.mutateAsync(payload);
      toast.success(t(($) => $.task_retry_policies.saved)); close();
    } catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.task_retry_policies.save_failed)); }
  };
  const remove = async (policy: TaskRetryPolicy) => {
    if (!window.confirm(t(($) => $.task_retry_policies.delete_confirm, { name: policy.name }))) return;
    try { await deletePolicy.mutateAsync(policy.id); toast.success(t(($) => $.task_retry_policies.deleted)); }
    catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.task_retry_policies.delete_failed)); }
  };
  const toggle = async (policy: TaskRetryPolicy, enabled: boolean) => {
    try { await updatePolicy.mutateAsync({ policyId: policy.id, enabled }); }
    catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.task_retry_policies.save_failed)); }
  };
  const matchLabel = (type: TaskRetryPolicyMatchType) => {
    switch (type) {
      case "http_status":
        return t(($) => $.task_retry_policies.match_types.http_status);
      case "error_contains":
        return t(($) => $.task_retry_policies.match_types.error_contains);
      default:
        return t(($) => $.task_retry_policies.match_types.failure_reason);
    }
  };

  return (
    <SettingsTab title={t(($) => $.task_retry_policies.title)} description={t(($) => $.task_retry_policies.description)}>
      <SettingsSection action={isOwner ? <Button size="sm" onClick={openCreate}><Plus className="mr-1 size-4" />{t(($) => $.task_retry_policies.add)}</Button> : null}>
        <SettingsCard>
          {isLoading ? <div className="p-8 text-center text-muted-foreground">{t(($) => $.task_retry_policies.loading)}</div> : isError ? <div className="p-8 text-center text-destructive">{t(($) => $.task_retry_policies.load_failed)}</div> : policies.length === 0 ? <div className="p-8 text-center text-muted-foreground">{t(($) => $.task_retry_policies.empty)}</div> : (
            <div className="divide-y divide-surface-border">
              {policies.map((policy) => (
                <div key={policy.id} className="flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:gap-4">
                  <div className="min-w-0 flex-1"><div className="flex items-center gap-2 font-medium"><span className={!policy.enabled ? "text-muted-foreground" : undefined}>{policy.name}</span>{!policy.enabled ? <span className="text-caption text-muted-foreground">({t(($) => $.task_retry_policies.disabled)})</span> : null}</div><div className="mt-1 text-caption text-muted-foreground">{t(($) => $.task_retry_policies.summary, { matchType: matchLabel(policy.match_type), matchValue: policy.match_value, attempts: t(($) => $.task_retry_policies.attempts, { count: policy.max_attempts }), delays: policy.delay_schedule.join(", ") })}</div></div>
                  {isOwner ? <div className="flex items-center gap-2"><Switch checked={policy.enabled} onCheckedChange={(value) => void toggle(policy, value)} aria-label={t(($) => $.task_retry_policies.enabled)} /><Button variant="ghost" size="icon-sm" onClick={() => openEdit(policy)} aria-label={t(($) => $.task_retry_policies.edit)}><Pencil className="size-4" /></Button><Button variant="ghost" size="icon-sm" onClick={() => void remove(policy)} aria-label={t(($) => $.task_retry_policies.delete)}><Trash2 className="size-4" /></Button></div> : null}
                </div>
              ))}
            </div>
          )}
        </SettingsCard>
        {!isOwner ? <p className="text-caption text-muted-foreground">{t(($) => $.task_retry_policies.owner_only)}</p> : null}
      </SettingsSection>
      <Dialog open={creating || !!editing} onOpenChange={(open) => !open && close()}>
        <DialogContent><DialogHeader><DialogTitle>{creating ? t(($) => $.task_retry_policies.create_title) : t(($) => $.task_retry_policies.edit_title)}</DialogTitle><DialogDescription>{t(($) => $.task_retry_policies.form_description)}</DialogDescription></DialogHeader>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1 sm:col-span-2"><Label>{t(($) => $.task_retry_policies.name)}</Label><Input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></div>
            <div className="space-y-1"><Label>{t(($) => $.task_retry_policies.match_type)}</Label><Select items={[{ value: "failure_reason", label: matchLabel("failure_reason") }, { value: "http_status", label: matchLabel("http_status") }, { value: "error_contains", label: matchLabel("error_contains") }]} value={draft.match_type} onValueChange={(value) => { if (value) setDraft({ ...draft, match_type: value as TaskRetryPolicyMatchType }); }}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="failure_reason">{matchLabel("failure_reason")}</SelectItem><SelectItem value="http_status">{matchLabel("http_status")}</SelectItem><SelectItem value="error_contains">{matchLabel("error_contains")}</SelectItem></SelectContent></Select></div>
            <div className="space-y-1"><Label>{t(($) => $.task_retry_policies.match_value)}</Label><Input value={draft.match_value} onChange={(e) => setDraft({ ...draft, match_value: e.target.value })} /></div>
            <div className="space-y-1"><Label>{t(($) => $.task_retry_policies.priority)}</Label><Input type="number" min="0" value={draft.priority} onChange={(e) => setDraft({ ...draft, priority: e.target.value })} /></div>
            <div className="space-y-1"><Label>{t(($) => $.task_retry_policies.max_attempts)}</Label><Input type="number" min="1" max="5" value={draft.max_attempts} onChange={(e) => setDraft({ ...draft, max_attempts: e.target.value })} /></div>
            <div className="space-y-1 sm:col-span-2"><Label>{t(($) => $.task_retry_policies.delay_schedule)}</Label><Input value={draft.delay_schedule} onChange={(e) => setDraft({ ...draft, delay_schedule: e.target.value })} placeholder={t(($) => $.task_retry_policies.delay_placeholder)} /><p className="text-caption text-muted-foreground">{t(($) => $.task_retry_policies.delay_hint)}</p></div>
            <label className="flex items-center gap-2 text-body sm:col-span-2"><Switch checked={draft.enabled} onCheckedChange={(enabled) => setDraft({ ...draft, enabled })} />{t(($) => $.task_retry_policies.enabled)}</label>
          </div>
          <DialogFooter><Button variant="outline" onClick={close}>{t(($) => $.task_retry_policies.cancel)}</Button><Button onClick={() => void save()} disabled={createPolicy.isPending || updatePolicy.isPending}>{createPolicy.isPending || updatePolicy.isPending ? <RotateCcw className="mr-1 size-4 animate-spin" /> : null}{t(($) => $.task_retry_policies.save)}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </SettingsTab>
  );
}
