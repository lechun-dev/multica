"use client";

/* eslint-disable i18next/no-literal-string -- Task permissions use canonical policy codes in API payloads and previews. */
/* eslint-disable no-restricted-syntax -- This isolated administration surface ships its fallback copy with the feature. */

import { useEffect, useMemo, useState } from "react";
import { Copy, ShieldCheck, UserMinus, Users } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { IssueAccessControlGrant, IssueAccessRequest, TaskAccessMode } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { ProjectMemberMultiSelect } from "../../projects/components/project-member-multi-select";
import { ProjectPermissionOrganizationTreeSelect } from "../../projects/components/project-permission-organization-tree-select";

type IssueAccessGrantsDialogProps = { issueId: string; projectId?: string | null; defaultOpen?: boolean };

function sameTaskGrant(left: IssueAccessControlGrant, right: IssueAccessControlGrant) {
  return left.subject_type === right.subject_type && left.subject_id === right.subject_id && left.role === right.role;
}

function mergeTaskGrants(current: IssueAccessControlGrant[], additions: IssueAccessControlGrant[]) {
  if (!additions.length) return current;
  return [
    ...current.filter((item) => !additions.some((candidate) => sameTaskGrant(candidate, item))),
    ...additions,
  ];
}

/** Task ACL editor plus a read-only resolver explanation for the current user. */
export function IssueAccessGrantsDialog({ issueId, projectId, defaultOpen = false }: IssueAccessGrantsDialogProps) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(defaultOpen);
  const [selectedUserIds, setSelectedUserIds] = useState<ReadonlySet<string>>(new Set());
  const [selectedOrganizationIds, setSelectedOrganizationIds] = useState<ReadonlySet<string>>(new Set());
  const [selectedEveryone, setSelectedEveryone] = useState(false);
  const [role, setRole] = useState("member");
  const [expiresAt, setExpiresAt] = useState("");
  const [mode, setMode] = useState<TaskAccessMode>("inherit");
  const [grants, setGrants] = useState<IssueAccessControlGrant[]>([]);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [shareUrl, setShareUrl] = useState("");
  const [grantsOpen, setGrantsOpen] = useState(false);
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

  useEffect(() => {
    if (!open || typeof window === "undefined") return;
    setShareUrl(window.location.href);
  }, [open]);

  const members = useMemo(() => membersQuery.data ?? [], [membersQuery.data]);
  const organizations = useMemo(() => directoryQuery.data?.organizations ?? [], [directoryQuery.data?.organizations]);
  const memberByUser = useMemo(() => new Map(members.map((member) => [member.user_id, member])), [members]);
  const organizationById = useMemo(() => new Map(organizations.map((organization) => [organization.id, organization])), [organizations]);
  const availableRoles = rolesQuery.data?.roles ?? [];
  const canManage = !!controlQuery.data;
  const effectivePermissions = useMemo(() => new Set(effectiveQuery.data?.permissions ?? []), [effectiveQuery.data?.permissions]);
  const permissionLabel = (permission: string) => {
    switch (permission) {
      case "project.view": return t(($) => $.permissions.view_task);
      case "project.edit": return t(($) => $.permissions.edit_task);
      case "project.issue.child.create": return t(($) => $.permissions.create_related_tasks);
      case "project.issue.comment": return t(($) => $.permissions.comment_task);
      case "project.issue.manage": return t(($) => $.permissions.manage_task);
      case "project.issue.archive": return t(($) => $.permissions.archive_task);
      case "project.agent.use": return t(($) => $.permissions.use_agent);
      default: return permission;
    }
  };
  const taskRoleLabel = (roleKey: string, fallback?: string) => {
    switch (roleKey) {
      case "viewer": return t(($) => $.permissions.task_access_level_viewer);
      case "member": return t(($) => $.permissions.task_access_level_editor);
      case "manager": return t(($) => $.permissions.task_access_level_manager);
      default: return fallback || roleKey;
    }
  };
  const accessSummary = [
    { permission: "project.view", label: t(($) => $.permissions.view_task) },
    { permission: "project.edit", label: t(($) => $.permissions.edit_task) },
    { permission: "project.issue.manage", label: t(($) => $.permissions.manage_task) },
  ];
  const allowedAccessLabels = accessSummary.filter((item) => effectivePermissions.has(item.permission)).map((item) => item.label);
  const readonlyReason = t(($) => $.permissions.task_permissions_readonly_manage);
  const taskPolicyDescription = projectId
    ? t(($) => $.permissions.task_policy_project_task, { version: controlQuery.data?.policy_version ?? "—" })
    : t(($) => $.permissions.task_policy_projectless_task, { version: controlQuery.data?.policy_version ?? "—" });
  const selectedCount = selectedEveryone ? 1 : selectedUserIds.size + selectedOrganizationIds.size;
  const pendingGrants = useMemo(() => {
    const expiry = expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {};
    // 2026-09-16 coder(lq): Preserve picker selections while "everyone" is on,
    // but save only the workspace-wide grant to avoid redundant ACL rows.
    if (selectedEveryone) return [{ subject_type: "everyone", role, scope: "task", ...expiry } satisfies IssueAccessControlGrant];
    return [
      ...[...selectedUserIds].map((subjectId): IssueAccessControlGrant => ({ subject_type: "user", subject_id: subjectId, role, scope: "task", ...expiry })),
      ...[...selectedOrganizationIds].map((subjectId): IssueAccessControlGrant => ({ subject_type: "organization", subject_id: subjectId, role, scope: "task", ...expiry })),
    ];
  }, [expiresAt, role, selectedEveryone, selectedOrganizationIds, selectedUserIds]);
  const grantsForSave = useMemo(() => mergeTaskGrants(grants, pendingGrants), [grants, pendingGrants]);
  const subjectName = (grant: IssueAccessControlGrant) => grant.subject_type === "everyone"
    ? t(($) => $.permissions.current_workspace_everyone)
    : grant.subject_type === "user"
      ? memberByUser.get(grant.subject_id || "")?.name || memberByUser.get(grant.subject_id || "")?.email || grant.subject_id || "—"
      : organizationById.get(grant.subject_id || "")?.name || grant.subject_id || "—";
  const resetPicker = () => { setSelectedUserIds(new Set()); setSelectedOrganizationIds(new Set()); setSelectedEveryone(false); setExpiresAt(""); };

  const copyShareLink = async () => {
    if (!shareUrl) return;
    try {
      await navigator.clipboard.writeText(shareUrl);
      toast.success(t(($) => $.permissions.task_link_copied));
    } catch {
      toast.error(t(($) => $.permissions.task_link_copy_failed));
    }
  };

  const requestPreview = async () => {
    if (!controlQuery.data) return;
    setSaving(true);
    try {
      setPreview(await api.previewIssueAccessControl(issueId, { expected_version: controlQuery.data.policy_version, project_access_mode: mode, grants: grantsForSave }));
      setPreviewOpen(true);
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setDirty(false); await controlQuery.refetch(); toast.error(t(($) => $.permissions.task_changed_reload)); }
      else toast.error(t(($) => $.permissions.task_preview_failed));
    } finally { setSaving(false); }
  };

  const confirmSave = async () => {
    if (!controlQuery.data) return;
    setSaving(true);
    try {
      const nextGrants = grantsForSave;
      await api.updateIssueAccessControl(issueId, { expected_version: controlQuery.data.policy_version, project_access_mode: mode, grants: nextGrants });
      setGrants(nextGrants);
      resetPicker();
      setDirty(false);
      setPreviewOpen(false);
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["issue-access-control", workspaceId, issueId] }), queryClient.invalidateQueries({ queryKey: ["issue-effective-access", workspaceId, issueId] })]);
      toast.success(t(($) => $.permissions.task_update_success));
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setPreviewOpen(false); setDirty(false); await controlQuery.refetch(); toast.error(t(($) => $.permissions.task_changed_reload)); }
      else toast.error(t(($) => $.permissions.task_save_failed));
    } finally { setSaving(false); }
  };

  const review = async (requestId: string, action: "approve" | "reject") => {
    const request = requestsQuery.data?.items.find((item) => item.id === requestId);
    try {
      const reviewed = await api.reviewIssueAccessRequest(issueId, requestId, { action });
      queryClient.setQueryData<{ items: IssueAccessRequest[] }>(["issue-access-requests", workspaceId, issueId], (current) => ({
        items: (current?.items ?? []).map((item) => item.id === requestId ? { ...item, ...reviewed, status: reviewed.status } : item),
      }));
      if (action === "approve" && request) {
        const approvedGrant: IssueAccessControlGrant = {
          subject_type: "user",
          subject_id: request.requester_user_id,
          role: request.requested_role,
          scope: "task",
          ...(request.expires_at ? { expires_at: request.expires_at } : {}),
        };
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["issue-access-control", workspaceId, issueId] }),
          queryClient.invalidateQueries({ queryKey: ["issue-effective-access", workspaceId, issueId] }),
        ]);
        setGrants((current) => [
          ...current.filter((item) => !(item.subject_type === approvedGrant.subject_type && item.subject_id === approvedGrant.subject_id && item.role === approvedGrant.role)),
          approvedGrant,
        ]);
      }
      await requestsQuery.refetch();
      toast.success(action === "approve" ? t(($) => $.permissions.task_request_approved) : t(($) => $.permissions.task_request_rejected));
    }
    catch { toast.error(t(($) => $.permissions.task_request_review_failed)); }
  };

  return <>
    <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground" onClick={() => setOpen(true)} aria-label={t(($) => $.permissions.task_permissions_title)}><ShieldCheck /></Button>} /><TooltipContent side="top">{t(($) => $.permissions.task_permissions_title)}</TooltipContent></Tooltip>
    <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (!next) { setDirty(false); setPreviewOpen(false); setGrantsOpen(false); } }}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl">
      <DialogHeader><DialogTitle>{t(($) => $.permissions.task_permissions_title)}</DialogTitle><DialogDescription>{t(($) => $.permissions.task_permissions_description)}</DialogDescription></DialogHeader>
      <section className="space-y-2">
        <h3 className="font-medium">{t(($) => $.permissions.task_link)}</h3>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input readOnly value={shareUrl} aria-label={t(($) => $.permissions.task_link)} className="font-mono text-caption" />
          <Button variant="outline" onClick={() => void copyShareLink()} className="sm:w-32"><Copy className="size-4" />{t(($) => $.permissions.copy_task_link)}</Button>
        </div>
      </section>
      {canManage ? <>
        <section className="space-y-3 border-t pt-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 className="font-medium">{t(($) => $.permissions.add_task_access)}</h3>
              <p className="text-caption text-muted-foreground">{t(($) => $.permissions.add_task_access_description)}</p>
            </div>
            <Button variant="outline" size="sm" onClick={() => setGrantsOpen(true)}><Users className="size-4" />{t(($) => $.permissions.direct_access_count, { count: grants.length })}</Button>
          </div>
          <div className="rounded-lg border bg-muted/10 p-3">
            <div className="space-y-3">
              <div className="text-caption font-medium text-muted-foreground">{t(($) => $.permissions.task_permission_object)}</div>
              <div className="grid gap-3 md:grid-cols-2">
                <div className="space-y-1.5">
                  <div className="text-caption text-muted-foreground">{t(($) => $.permissions.user)}</div>
                  <div className="min-w-0"><ProjectMemberMultiSelect members={members} selectedIds={selectedUserIds} onToggle={(id) => setSelectedUserIds((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onSelectAll={(ids) => setSelectedUserIds(new Set(ids))} onClear={() => setSelectedUserIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_people)} selectedLabel={t(($) => $.permissions.task_permission_people_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_results)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.workspace_members_failed)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={membersQuery.isLoading} hasError={membersQuery.isError} disabled={selectedEveryone} ariaLabel={t(($) => $.permissions.task_permission_select_people)} /></div>
                </div>
                <div className="space-y-1.5">
                  <div className="text-caption text-muted-foreground">{t(($) => $.permissions.organization)}</div>
                  <div className="min-w-0"><ProjectPermissionOrganizationTreeSelect organizations={organizations} selectedIds={selectedOrganizationIds} onToggle={(id) => setSelectedOrganizationIds((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onSelectAll={(ids) => setSelectedOrganizationIds(new Set(ids))} onClear={() => setSelectedOrganizationIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_departments)} selectedLabel={t(($) => $.permissions.task_permission_organizations_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_organizations)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.no_organizations)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={directoryQuery.isLoading} hasError={directoryQuery.isError} disabled={selectedEveryone} ariaLabel={t(($) => $.permissions.task_permission_select_departments)} /></div>
                </div>
                <label className="flex min-h-9 cursor-pointer items-center gap-2 rounded-md border bg-background px-3 py-2 md:col-span-2">
                  <Checkbox checked={selectedEveryone} onCheckedChange={(checked) => setSelectedEveryone(checked === true)} aria-label={t(($) => $.permissions.everyone)} />
                  <span className="font-medium">{t(($) => $.permissions.everyone)}</span>
                  <span className="text-caption text-muted-foreground">{t(($) => $.permissions.current_workspace_everyone)}</span>
                </label>
              </div>
              <div className="space-y-2 border-t pt-3">
                <div className="text-caption font-medium text-muted-foreground">{t(($) => $.permissions.task_grant_settings)}</div>
                <div className="grid gap-2 md:grid-cols-[10rem_minmax(0,1fr)]">
                  <Select modal={false} items={availableRoles.map((item) => ({ value: item.key, label: taskRoleLabel(item.key, item.name) }))} value={role} onValueChange={(value) => setRole(value || "member")}><SelectTrigger aria-label={t(($) => $.permissions.task_role)}><SelectValue /></SelectTrigger><SelectContent>{availableRoles.map((item) => <SelectItem key={item.key} value={item.key}>{taskRoleLabel(item.key, item.name)}</SelectItem>)}</SelectContent></Select>
                  <Input type="datetime-local" aria-label={t(($) => $.permissions.task_grant_expiry)} value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} />
                </div>
              </div>
            </div>
          </div>
        </section>
        <section className="space-y-3 border-t pt-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 className="font-medium">{t(($) => $.permissions.task_policy)}</h3>
              <p className="text-caption text-muted-foreground">{taskPolicyDescription}</p>
            </div>
            <Select modal={false} items={[{ value: "inherit", label: t(($) => $.permissions.task_policy_inherit) }, { value: "restricted", label: t(($) => $.permissions.task_policy_restricted) }]} value={mode} onValueChange={(value) => { setMode(value as TaskAccessMode); setDirty(true); }}><SelectTrigger className="w-48" aria-label={t(($) => $.permissions.task_project_access_mode)}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="inherit">{t(($) => $.permissions.task_policy_inherit)}</SelectItem><SelectItem value="restricted">{t(($) => $.permissions.task_policy_restricted)}</SelectItem></SelectContent></Select>
          </div>
        </section>
        {requestsQuery.data?.items.some((item) => item.status === "pending") ? <section className="space-y-2 border-t pt-4"><h3 className="font-medium">{t(($) => $.permissions.task_pending_requests)}</h3>{requestsQuery.data.items.filter((item) => item.status === "pending").map((request) => <div key={request.id} className="flex flex-wrap items-center gap-2 rounded-md border p-2"><code>{request.requester_user_id}</code><span>{t(($) => $.permissions.task_request_role, { role: taskRoleLabel(request.requested_role) })}</span><span className="flex-1 text-muted-foreground">{request.reason}</span><Button size="sm" variant="brandSubtle" onClick={() => void review(request.id, "approve")}>{t(($) => $.permissions.task_request_approve)}</Button><Button size="sm" variant="outline" onClick={() => void review(request.id, "reject")}>{t(($) => $.permissions.task_request_reject)}</Button></div>)}</section> : null}
      </> : <p className="rounded-md border p-3 text-caption text-muted-foreground">{readonlyReason}</p>}
      <section className="border-t pt-3">
        <div className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
          <span className="font-medium text-foreground">{t(($) => $.permissions.task_access_summary_title)}</span>
          <span>{allowedAccessLabels.length ? allowedAccessLabels.join("、") : t(($) => $.permissions.task_access_summary_none)}</span>
        </div>
      </section>
      <DialogFooter><Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.close)}</Button>{canManage ? <Button variant="brand" onClick={() => void requestPreview()} disabled={(!dirty && selectedCount === 0) || saving}>{saving ? t(($) => $.permissions.task_checking) : t(($) => $.permissions.task_preview_save)}</Button> : null}</DialogFooter>
    </DialogContent></Dialog>
    <Dialog open={grantsOpen} onOpenChange={setGrantsOpen}><DialogContent className="max-h-[80vh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader><DialogTitle>{t(($) => $.permissions.direct_access)}</DialogTitle><DialogDescription>{t(($) => $.permissions.existing_task_access_description)}</DialogDescription></DialogHeader>
      <div className="overflow-x-auto rounded-lg border"><table className="w-full min-w-[620px] text-body"><thead className="bg-muted/40 text-left text-caption text-muted-foreground"><tr><th className="px-3 py-2">{t(($) => $.permissions.authorization_subject)}</th><th className="px-3 py-2">{t(($) => $.permissions.task_role)}</th><th className="px-3 py-2">{t(($) => $.permissions.task_exact_permissions)}</th><th className="px-3 py-2">{t(($) => $.permissions.task_expires)}</th><th className="w-12" /></tr></thead><tbody>{grants.length ? grants.map((grant, index) => { const definition = availableRoles.find((item) => item.key === grant.role); return <tr key={`${grant.subject_type}-${grant.subject_id}-${grant.role}-${index}`} className="border-t"><td className="px-3 py-2">{subjectName(grant)}</td><td className="px-3 py-2">{taskRoleLabel(grant.role, definition?.name)}</td><td className="px-3 py-2 text-caption text-muted-foreground">{definition?.permissions.map(permissionLabel).join(", ") || "—"}</td><td className="px-3 py-2">{grant.expires_at ? new Date(grant.expires_at).toLocaleString() : t(($) => $.permissions.task_diagnostic_never)}</td><td><Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_task_access_aria)} ${subjectName(grant)}`} onClick={() => { setGrants((current) => current.filter((_, itemIndex) => itemIndex !== index)); setDirty(true); }}><UserMinus className="size-3.5" /></Button></td></tr>; }) : <tr><td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">{t(($) => $.permissions.no_direct_access)}</td></tr>}</tbody></table></div>
      <DialogFooter><Button variant="outline" onClick={() => setGrantsOpen(false)}>{t(($) => $.permissions.close)}</Button></DialogFooter>
    </DialogContent></Dialog>
    <Dialog open={previewOpen} onOpenChange={setPreviewOpen}><DialogContent><DialogHeader><DialogTitle>{t(($) => $.permissions.task_preview_title)}</DialogTitle><DialogDescription>{t(($) => $.permissions.task_preview_description, { version: controlQuery.data?.policy_version ?? "—" })}</DialogDescription></DialogHeader><div className="space-y-2 text-body"><p>{t(($) => $.permissions.task_preview_mode)} <code>{preview?.before.project_access_mode}</code> → <code>{preview?.after.project_access_mode}</code></p><p>{t(($) => $.permissions.task_preview_losing_access)} {preview?.subjects_losing_access.join(", ") || t(($) => $.permissions.task_preview_none)}</p><p>{t(($) => $.permissions.task_preview_other_source)} {preview?.subjects_with_other_source.join(", ") || t(($) => $.permissions.task_preview_none)}</p><p>{t(($) => $.permissions.task_preview_affected)} {preview?.affected_effects.join(", ") || t(($) => $.permissions.task_preview_none)}</p></div><DialogFooter><Button variant="outline" onClick={() => setPreviewOpen(false)}>{t(($) => $.permissions.task_preview_back)}</Button><Button variant="brand" onClick={() => void confirmSave()} disabled={saving}>{t(($) => $.permissions.task_preview_confirm)}</Button></DialogFooter></DialogContent></Dialog>
  </>;
}
