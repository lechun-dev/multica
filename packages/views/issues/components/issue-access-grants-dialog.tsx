"use client";

/* eslint-disable i18next/no-literal-string -- Permission diagnostics deliberately expose canonical policy and source names. */
/* eslint-disable no-restricted-syntax -- This isolated administration surface ships its fallback copy with the feature. */

import { useEffect, useMemo, useState } from "react";
import { ShieldCheck, UserMinus } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { IssueAccessControlGrant, TaskAccessMode, TaskAccessSubjectType } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { ProjectMemberMultiSelect } from "../../projects/components/project-member-multi-select";
import { ProjectPermissionOrganizationTreeSelect } from "../../projects/components/project-permission-organization-tree-select";

type IssueAccessGrantsDialogProps = { issueId: string; projectId?: string | null };

const sourceLabel = (source: string) => ({
  project_direct: "Project · direct", project_organization: "Project · organization",
  project_everyone: "Project · Everyone", parent_issue: "Direct parent task",
  issue_direct: "Task · direct", issue_organization: "Task · organization",
  issue_everyone: "Task · Everyone", creator: "Task creator", assignee: "Assignee",
  mention: "@mention", delegated_originator: "Original requester",
  access_request: "Approved access request", workspace_owner_bypass: "Workspace Owner bypass",
}[source] ?? source);

/** Task ACL editor plus a read-only resolver explanation for the current user. */
export function IssueAccessGrantsDialog({ issueId, projectId }: IssueAccessGrantsDialogProps) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [subjectType, setSubjectType] = useState<TaskAccessSubjectType>("user");
  const [selectedUserIds, setSelectedUserIds] = useState<ReadonlySet<string>>(new Set());
  const [selectedOrganizationIds, setSelectedOrganizationIds] = useState<ReadonlySet<string>>(new Set());
  const [role, setRole] = useState("member");
  const [expiresAt, setExpiresAt] = useState("");
  const [mode, setMode] = useState<TaskAccessMode>("inherit");
  const [grants, setGrants] = useState<IssueAccessControlGrant[]>([]);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [preview, setPreview] = useState<Awaited<ReturnType<typeof api.previewIssueAccessControl>> | null>(null);

  const controlQuery = useQuery({ queryKey: ["issue-access-control", workspaceId, issueId], queryFn: () => api.getIssueAccessControl(issueId), enabled: open, retry: false });
  const effectiveQuery = useQuery({ queryKey: ["issue-effective-access", workspaceId, issueId], queryFn: () => api.getIssueEffectiveAccess(issueId), enabled: open, retry: false });
  const rolesQuery = useQuery({ queryKey: ["task-permission-roles", workspaceId], queryFn: () => api.listTaskPermissionRoles(), enabled: open && !!workspaceId });
  const directoryQuery = useQuery({ queryKey: ["project-permission-organizations", workspaceId], queryFn: () => api.listProjectAuthorizationOrganizations(workspaceId), enabled: open && !!workspaceId, staleTime: 60_000 });
  const membersQuery = useQuery({ ...memberListOptions(workspaceId), enabled: open && !!workspaceId });
  const requestsQuery = useQuery({ queryKey: ["issue-access-requests", workspaceId, issueId], queryFn: () => api.listIssueAccessRequests(issueId), enabled: open && !!controlQuery.data, retry: false });

  useEffect(() => {
    if (!controlQuery.data || dirty) return;
    setMode(controlQuery.data.project_access_mode);
    setGrants(controlQuery.data.grants);
  }, [controlQuery.data, dirty]);

  const members = useMemo(() => membersQuery.data ?? [], [membersQuery.data]);
  const organizations = useMemo(() => directoryQuery.data?.organizations ?? [], [directoryQuery.data?.organizations]);
  const memberByUser = useMemo(() => new Map(members.map((member) => [member.user_id, member])), [members]);
  const organizationById = useMemo(() => new Map(organizations.map((organization) => [organization.id, organization])), [organizations]);
  const availableRoles = rolesQuery.data?.roles ?? [];
  const canManage = !!controlQuery.data;
  const subjectTypes = useMemo<Array<{ value: TaskAccessSubjectType; label: string }>>(() => [
    { value: "user", label: t(($) => $.permissions.user) },
    { value: "organization", label: t(($) => $.permissions.organization) },
    { value: "everyone", label: t(($) => $.permissions.everyone) },
  ], [t]);
  const selectedCount = subjectType === "user" ? selectedUserIds.size : subjectType === "organization" ? selectedOrganizationIds.size : 1;
  const subjectName = (grant: IssueAccessControlGrant) => grant.subject_type === "everyone"
    ? t(($) => $.permissions.current_workspace_everyone)
    : grant.subject_type === "user"
      ? memberByUser.get(grant.subject_id || "")?.name || memberByUser.get(grant.subject_id || "")?.email || grant.subject_id || "—"
      : organizationById.get(grant.subject_id || "")?.name || grant.subject_id || "—";
  const resetPicker = () => { setSelectedUserIds(new Set()); setSelectedOrganizationIds(new Set()); setExpiresAt(""); };

  const addGrant = () => {
    const ids = subjectType === "user" ? [...selectedUserIds] : subjectType === "organization" ? [...selectedOrganizationIds] : [""];
    if (!ids.length) { toast.error(t(($) => $.permissions.task_authorization_subject_required)); return; }
    const additions = ids.map((subjectId): IssueAccessControlGrant => ({ subject_type: subjectType, ...(subjectId ? { subject_id: subjectId } : {}), role, scope: "task", ...(expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {}) }));
    setGrants((current) => [...current.filter((item) => !additions.some((candidate) => candidate.subject_type === item.subject_type && candidate.subject_id === item.subject_id && candidate.role === item.role)), ...additions]);
    setDirty(true);
    resetPicker();
  };

  const requestPreview = async () => {
    if (!controlQuery.data) return;
    setSaving(true);
    try {
      setPreview(await api.previewIssueAccessControl(issueId, { expected_version: controlQuery.data.policy_version, project_access_mode: mode, grants }));
      setPreviewOpen(true);
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setDirty(false); await controlQuery.refetch(); toast.error("Permissions changed elsewhere. Latest policy reloaded; review and try again."); }
      else toast.error("Unable to preview this permission change. Your edits were not saved.");
    } finally { setSaving(false); }
  };

  const confirmSave = async () => {
    if (!controlQuery.data) return;
    setSaving(true);
    try {
      await api.updateIssueAccessControl(issueId, { expected_version: controlQuery.data.policy_version, project_access_mode: mode, grants });
      setDirty(false);
      setPreviewOpen(false);
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["issue-access-control", workspaceId, issueId] }), queryClient.invalidateQueries({ queryKey: ["issue-effective-access", workspaceId, issueId] })]);
      toast.success("Task permissions updated");
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setPreviewOpen(false); setDirty(false); await controlQuery.refetch(); toast.error("Permissions changed elsewhere. Latest policy reloaded; review and try again."); }
      else toast.error("Task permissions were not saved. Your edits remain available to retry.");
    } finally { setSaving(false); }
  };

  const review = async (requestId: string, action: "approve" | "reject") => {
    try { await api.reviewIssueAccessRequest(issueId, requestId, { action }); await requestsQuery.refetch(); toast.success(action === "approve" ? "Access request approved" : "Access request rejected"); }
    catch { toast.error("The request could not be reviewed. Refresh and try again."); }
  };

  return <>
    <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground" onClick={() => setOpen(true)} aria-label={t(($) => $.permissions.task_permissions_title)}><ShieldCheck /></Button>} /><TooltipContent side="top">{t(($) => $.permissions.task_permissions_title)}</TooltipContent></Tooltip>
    <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (!next) { setDirty(false); setPreviewOpen(false); } }}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl">
      <DialogHeader><DialogTitle>{t(($) => $.permissions.task_permissions_title)}</DialogTitle><DialogDescription>Task roles are independent from project roles. Project permissions are projected only in inherit mode.</DialogDescription></DialogHeader>
      <section className="space-y-2"><h3 className="font-medium">My effective permissions</h3><p className="text-caption text-muted-foreground">Mode: <code>{effectiveQuery.data?.project_access_mode ?? "—"}</code>. Direct-parent access still applies in both modes; restricted only removes this task’s project source.</p>
        <div className="overflow-x-auto rounded-lg border"><table className="w-full min-w-[720px] text-body"><thead className="bg-muted/40 text-left text-caption text-muted-foreground"><tr><th className="px-3 py-2">Permission</th><th className="px-3 py-2">Source</th><th className="px-3 py-2">Role scope</th><th className="px-3 py-2">Source resource</th><th className="px-3 py-2">Expires</th></tr></thead><tbody>
          {effectiveQuery.data?.sources.length ? effectiveQuery.data.sources.map((source, index) => <tr key={`${source.permission}-${source.source}-${source.grant_id ?? index}`} className="border-t"><td className="px-3 py-2">{source.permission}</td><td className="px-3 py-2">{sourceLabel(source.source)}</td><td className="px-3 py-2">{source.scope ? `${source.scope}:${source.role || "—"}` : "—"}</td><td className="px-3 py-2"><code>{source.source_resource.scope}:{source.source_resource.id}</code></td><td className="px-3 py-2">{source.expires_at ? new Date(source.expires_at).toLocaleString() : "Never"}</td></tr>) : <tr><td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">No effective permission sources.</td></tr>}
        </tbody></table></div>
      </section>
      {canManage ? <>
        <section className="space-y-3 border-t pt-3"><div className="flex flex-wrap items-center justify-between gap-2"><div><h3 className="font-medium">Task policy</h3><p className="text-caption text-muted-foreground">Policy version {controlQuery.data.policy_version}{projectId ? " · Project task" : " · Projectless task"}</p></div><Select modal={false} items={[{ value: "inherit", label: "inherit" }, { value: "restricted", label: "restricted" }]} value={mode} onValueChange={(value) => { setMode(value as TaskAccessMode); setDirty(true); }}><SelectTrigger className="w-40" aria-label="Task project access mode"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="inherit">inherit</SelectItem><SelectItem value="restricted">restricted</SelectItem></SelectContent></Select></div>
          <div className="overflow-x-auto rounded-lg border"><table className="w-full min-w-[620px] text-body"><thead className="bg-muted/40 text-left text-caption text-muted-foreground"><tr><th className="px-3 py-2">Subject</th><th className="px-3 py-2">Task role</th><th className="px-3 py-2">Exact permissions (read-only)</th><th className="px-3 py-2">Expires</th><th className="w-12" /></tr></thead><tbody>{grants.length ? grants.map((grant, index) => { const definition = availableRoles.find((item) => item.key === grant.role); return <tr key={`${grant.subject_type}-${grant.subject_id}-${grant.role}-${index}`} className="border-t"><td className="px-3 py-2">{subjectName(grant)}</td><td className="px-3 py-2">{definition?.name ?? grant.role} <code className="text-caption">(task)</code></td><td className="px-3 py-2 text-caption text-muted-foreground">{definition?.permissions.join(", ") || "—"}</td><td className="px-3 py-2">{grant.expires_at ? new Date(grant.expires_at).toLocaleString() : "Never"}</td><td><Button variant="ghost" size="icon-sm" aria-label={`Remove access ${subjectName(grant)}`} onClick={() => { setGrants((current) => current.filter((_, itemIndex) => itemIndex !== index)); setDirty(true); }}><UserMinus className="size-3.5" /></Button></td></tr>; }) : <tr><td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">No manual task grants.</td></tr>}</tbody></table></div>
        </section>
        <section className="space-y-2 border-t pt-3"><h3 className="font-medium">Add task-role grant</h3><div className="flex flex-col gap-2 sm:flex-row sm:items-start">
          <Select modal={false} items={subjectTypes} value={subjectType} onValueChange={(value) => { setSubjectType(value as TaskAccessSubjectType); resetPicker(); }}><SelectTrigger className="w-full sm:w-36" aria-label={t(($) => $.permissions.task_permission_object_type)}><SelectValue /></SelectTrigger><SelectContent>{subjectTypes.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
          {subjectType === "user" ? <div className="min-w-0 flex-1"><ProjectMemberMultiSelect members={members} selectedIds={selectedUserIds} onToggle={(id) => setSelectedUserIds((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onSelectAll={(ids) => setSelectedUserIds(new Set(ids))} onClear={() => setSelectedUserIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_people)} selectedLabel={t(($) => $.permissions.task_permission_people_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_results)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.workspace_members_failed)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={membersQuery.isLoading} hasError={membersQuery.isError} ariaLabel={t(($) => $.permissions.task_permission_select_people)} /></div> : subjectType === "organization" ? <div className="min-w-0 flex-1"><ProjectPermissionOrganizationTreeSelect organizations={organizations} selectedIds={selectedOrganizationIds} onToggle={(id) => setSelectedOrganizationIds((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onSelectAll={(ids) => setSelectedOrganizationIds(new Set(ids))} onClear={() => setSelectedOrganizationIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_departments)} selectedLabel={t(($) => $.permissions.task_permission_organizations_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_organizations)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.no_organizations)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={directoryQuery.isLoading} hasError={directoryQuery.isError} ariaLabel={t(($) => $.permissions.task_permission_select_departments)} /></div> : <div className="flex min-h-9 flex-1 items-center rounded-md border px-3 text-caption text-muted-foreground">{t(($) => $.permissions.current_workspace_everyone)}</div>}
          <Select modal={false} items={availableRoles.map((item) => ({ value: item.key, label: item.name }))} value={role} onValueChange={(value) => setRole(value || "member")}><SelectTrigger className="w-full sm:w-40" aria-label={t(($) => $.permissions.task_role)}><SelectValue /></SelectTrigger><SelectContent>{availableRoles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name}</SelectItem>)}</SelectContent></Select><Input type="datetime-local" className="w-full sm:w-48" aria-label="Grant expiry" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /><Button onClick={addGrant} disabled={!selectedCount}>Add</Button>
        </div></section>
        {requestsQuery.data?.items.some((item) => item.status === "pending") ? <section className="space-y-2 border-t pt-3"><h3 className="font-medium">Pending access requests</h3>{requestsQuery.data.items.filter((item) => item.status === "pending").map((request) => <div key={request.id} className="flex flex-wrap items-center gap-2 rounded-md border p-2"><code>{request.requester_user_id}</code><span>requests task:{request.requested_role}</span><span className="flex-1 text-muted-foreground">{request.reason}</span><Button size="sm" onClick={() => void review(request.id, "approve")}>Approve</Button><Button size="sm" variant="outline" onClick={() => void review(request.id, "reject")}>Reject</Button></div>)}</section> : null}
      </> : <p className="rounded-md border p-3 text-caption text-muted-foreground">You can inspect your effective access, but only a user with task Manage permission can change sharing.</p>}
      <DialogFooter><Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.close)}</Button>{canManage ? <Button onClick={() => void requestPreview()} disabled={!dirty || saving}>{saving ? "Checking…" : "Preview & save"}</Button> : null}</DialogFooter>
    </DialogContent></Dialog>
    <Dialog open={previewOpen} onOpenChange={setPreviewOpen}><DialogContent><DialogHeader><DialogTitle>Confirm task permission changes</DialogTitle><DialogDescription>This server preview is based on policy version {controlQuery.data?.policy_version}.</DialogDescription></DialogHeader><div className="space-y-2 text-body"><p>Mode: <code>{preview?.before.project_access_mode}</code> → <code>{preview?.after.project_access_mode}</code></p><p>Subjects losing this direct source: {preview?.subjects_losing_access.join(", ") || "none"}</p><p>Subjects retaining another listed source: {preview?.subjects_with_other_source.join(", ") || "none"}</p><p>Affected: {preview?.affected_effects.join(", ") || "none"}</p></div><DialogFooter><Button variant="outline" onClick={() => setPreviewOpen(false)}>Back</Button><Button onClick={() => void confirmSave()} disabled={saving}>Confirm update</Button></DialogFooter></DialogContent></Dialog>
  </>;
}
