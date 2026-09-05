"use client";

import { useMemo, useState } from "react";
import { ShieldCheck, UserMinus } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { ProjectAccessGrantSubjectType } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { ProjectMemberMultiSelect } from "../../projects/components/project-member-multi-select";
import { ProjectPermissionOrganizationTreeSelect } from "../../projects/components/project-permission-organization-tree-select";

type IssueAccessGrantsDialogProps = { issueId: string; projectId: string };

/**
 * Task authorization deliberately has its own dialog. Project grants are
 * read-only context here; writes always include this issue ID and use a role.
 * 2026-09-04 coder(lq): Keep task grants additive and API-compatible while
 * making the existing-grants audit list and bulk subject picker explicit.
 */
export function IssueAccessGrantsDialog({ issueId, projectId }: IssueAccessGrantsDialogProps) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [subjectType, setSubjectType] = useState<ProjectAccessGrantSubjectType>("user");
  const [selectedUserIds, setSelectedUserIds] = useState<ReadonlySet<string>>(new Set());
  const [selectedOrganizationIds, setSelectedOrganizationIds] = useState<ReadonlySet<string>>(new Set());
  const [role, setRole] = useState("member");
  const [saving, setSaving] = useState(false);

  const grantsQuery = useQuery({ queryKey: ["issue-access-grants", workspaceId, issueId, projectId], queryFn: () => api.listIssueAccessGrants(issueId), enabled: open });
  const rolesQuery = useQuery({ queryKey: ["project-permission-roles", workspaceId], queryFn: () => api.listProjectPermissionRoles(), enabled: open && !!workspaceId });
  const directoryQuery = useQuery({ queryKey: ["project-permission-organizations", workspaceId], queryFn: () => api.listProjectAuthorizationOrganizations(workspaceId), enabled: open && !!workspaceId, staleTime: 60_000 });
  const membersQuery = useQuery({ ...memberListOptions(workspaceId), enabled: open && !!workspaceId });
  const members = useMemo(() => membersQuery.data ?? [], [membersQuery.data]);
  const organizations = useMemo(() => directoryQuery.data?.organizations ?? [], [directoryQuery.data?.organizations]);
  const memberByUser = useMemo(() => new Map(members.map((member) => [member.user_id, member])), [members]);
  const organizationById = useMemo(() => new Map(organizations.map((organization) => [organization.id, organization])), [organizations]);
  const roleByKey = useMemo(() => new Map((rolesQuery.data?.roles ?? []).map((item) => [item.key, item])), [rolesQuery.data?.roles]);
  const grants = grantsQuery.data?.grants ?? [];
  const taskRoles = useMemo(() => [
    { value: "viewer", label: t(($) => $.permissions.role_viewer) },
    { value: "member", label: t(($) => $.permissions.role_member) },
    { value: "manager", label: t(($) => $.permissions.role_manager) },
  ], [t]);
  const availableRoles = useMemo(() => {
    const taskPermissionKeys = new Set(["project.view", "project.edit", "project.issue.comment", "project.issue.manage", "project.issue.archive", "project.agent.use"]);
    const roles = rolesQuery.data?.roles?.filter((item) => item.permissions.length === 0 || item.permissions.every((permission) => taskPermissionKeys.has(permission))) ?? [];
    return roles.length ? roles.map((item) => ({ value: item.key, label: item.name || item.key })) : taskRoles;
  }, [rolesQuery.data?.roles, taskRoles]);
  const subjectTypes = useMemo<Array<{ value: ProjectAccessGrantSubjectType; label: string }>>(() => [
    { value: "user", label: t(($) => $.permissions.user) },
    { value: "organization", label: t(($) => $.permissions.organization) },
    { value: "everyone", label: t(($) => $.permissions.everyone) },
  ], [t]);

  const reset = () => { setSubjectType("user"); setSelectedUserIds(new Set()); setSelectedOrganizationIds(new Set()); setRole("member"); };
  const selectedSubjectCount = subjectType === "user" ? selectedUserIds.size : subjectType === "organization" ? selectedOrganizationIds.size : 1;
  const canCreate = subjectType === "everyone" || selectedSubjectCount > 0;

  const createGrant = async () => {
    if (!canCreate) { toast.error(t(($) => $.permissions.task_authorization_subject_required)); return; }
    const subjects = subjectType === "user"
      ? [...selectedUserIds].map((subject_id) => ({ subject_type: "user" as const, subject_id }))
      : subjectType === "organization"
        ? [...selectedOrganizationIds].map((subject_id) => ({ subject_type: "organization" as const, subject_id }))
        : [{ subject_type: "everyone" as const }];
    setSaving(true);
    try {
      const results = await Promise.allSettled(subjects.map((subject) => api.createIssueAccessGrant(issueId, { ...subject, role })));
      const failed = results.filter((result) => result.status === "rejected");
      await queryClient.invalidateQueries({ queryKey: ["issue-access-grants", workspaceId, issueId, projectId] });
      if (failed.length > 0) { toast.error(t(($) => $.permissions.task_authorization_add_failed)); return; }
      reset();
      toast.success(t(($) => $.permissions.task_authorization_added));
    } finally { setSaving(false); }
  };

  const revoke = async (grant: (typeof grants)[number]) => {
    try {
      await api.revokeIssueAccessGrant(issueId, { subject_type: grant.subject_type, subject_id: grant.subject_id, role: grant.role, permission: grant.permission });
      await queryClient.invalidateQueries({ queryKey: ["issue-access-grants", workspaceId, issueId, projectId] });
      toast.success(t(($) => $.permissions.task_authorization_removed));
    } catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.permissions.task_authorization_remove_failed)); }
  };

  const formatGrantTime = (value?: string) => {
    if (!value) return "—";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(date);
  };
  const grantSubjectName = (grant: (typeof grants)[number]) => grant.subject_type === "everyone"
    ? t(($) => $.permissions.current_workspace_everyone)
    : grant.subject_type === "user"
      ? memberByUser.get(grant.subject_id || "")?.name || memberByUser.get(grant.subject_id || "")?.email || grant.subject_id || "—"
      : grant.subject_type === "organization"
        ? organizationById.get(grant.subject_id || "")?.name || grant.subject_id || "—"
        : grant.subject_id || "—";
  const grantRoleName = (grant: (typeof grants)[number]) => grant.role ? roleByKey.get(grant.role)?.name || taskRoles.find((item) => item.value === grant.role)?.label || grant.role : grant.permission || "—";

  return <>
    <Tooltip>
      <TooltipTrigger render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground" onClick={() => setOpen(true)} aria-label={t(($) => $.permissions.task_permissions_title)}><ShieldCheck /></Button>} />
      <TooltipContent side="top">{t(($) => $.permissions.task_permissions_title)}</TooltipContent>
    </Tooltip>
    <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (!next) reset(); }}>
      <DialogContent className="sm:max-w-4xl">
        <DialogHeader><DialogTitle>{t(($) => $.permissions.task_permissions_title)}</DialogTitle><DialogDescription>{t(($) => $.permissions.task_permissions_description)}</DialogDescription></DialogHeader>
        <section className="space-y-2">
          <h3 className="text-body font-medium">{t(($) => $.permissions.current_access)}</h3>
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[620px] text-body">
              <thead className="bg-muted/40 text-left text-caption text-muted-foreground"><tr><th className="px-3 py-2 font-medium">{t(($) => $.permissions.task_permission_name)}</th><th className="px-3 py-2 font-medium">{t(($) => $.permissions.task_permission_source)}</th><th className="px-3 py-2 font-medium">{t(($) => $.permissions.task_permission_role)}</th><th className="px-3 py-2 font-medium">{t(($) => $.permissions.task_permission_granted_at)}</th><th className="w-12 px-3 py-2" /></tr></thead>
              <tbody>{grants.length === 0 ? <tr><td colSpan={5} className="px-3 py-6 text-center text-caption text-muted-foreground">{t(($) => $.permissions.current_access_empty)}</td></tr> : grants.map((grant) => {
                const inherited = grant.issue_id !== issueId;
                // 2026-09-05 coder(lq): A task creator's implicit Owner is a
                // hard invariant, so do not render a revoke action that the
                // API must reject when no historical grant row exists.
                const isImmutableCreatorOwner = grant.issue_id === issueId
                  && grant.subject_type === "user"
                  && grant.role === "owner"
                  && (grant.source === "system" || grant.source === "migration");
                const subjectLabel = subjectTypes.find((item) => item.value === grant.subject_type)?.label || grant.subject_type;
                return <tr key={`${grant.id}-${grant.issue_id || "project"}`} className="border-t"><td className="px-3 py-2"><div className="font-medium">{grantSubjectName(grant)}</div><div className="text-caption text-muted-foreground">{subjectLabel}</div></td><td className="px-3 py-2 text-muted-foreground">{inherited ? t(($) => $.permissions.inherited_from_project) : t(($) => $.permissions.direct_task_grant)}</td><td className="px-3 py-2">{grantRoleName(grant)}</td><td className="px-3 py-2 text-muted-foreground">{formatGrantTime(grant.created_at)}</td><td className="px-3 py-2">{!inherited && !isImmutableCreatorOwner ? <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_task_access_aria)} ${grantSubjectName(grant)}`} onClick={() => void revoke(grant)}><UserMinus className="size-3.5" /></Button> : null}</td></tr>;
              })}</tbody>
            </table>
          </div>
        </section>
        <section className="space-y-2 border-t pt-3">
          <h3 className="text-body font-medium">{t(($) => $.permissions.add_task_access)}</h3>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
            <Select modal={false} items={subjectTypes} value={subjectType} onValueChange={(value) => { const next = (value as ProjectAccessGrantSubjectType) || "user"; setSubjectType(next); setSelectedUserIds(new Set()); setSelectedOrganizationIds(new Set()); }}><SelectTrigger className="w-full sm:w-36" aria-label={t(($) => $.permissions.task_permission_object_type)}><SelectValue /></SelectTrigger><SelectContent alignItemWithTrigger={false}>{subjectTypes.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
            {subjectType === "user" ? <div className="min-w-0 flex-1"><ProjectMemberMultiSelect members={members} selectedIds={selectedUserIds} onToggle={(userId) => setSelectedUserIds((current) => { const next = new Set(current); if (next.has(userId)) next.delete(userId); else next.add(userId); return next; })} onSelectAll={(ids) => setSelectedUserIds((current) => new Set([...current, ...ids]))} onClear={() => setSelectedUserIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_people)} selectedLabel={t(($) => $.permissions.task_permission_people_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_results)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.workspace_members_failed)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={membersQuery.isLoading} hasError={membersQuery.isError} ariaLabel={t(($) => $.permissions.task_permission_select_people)} /></div> : subjectType === "organization" ? <div className="min-w-0 flex-1"><ProjectPermissionOrganizationTreeSelect organizations={organizations} selectedIds={selectedOrganizationIds} onToggle={(organizationId) => setSelectedOrganizationIds((current) => { const next = new Set(current); if (next.has(organizationId)) next.delete(organizationId); else next.add(organizationId); return next; })} onSelectAll={(ids) => setSelectedOrganizationIds((current) => new Set([...current, ...ids]))} onClear={() => setSelectedOrganizationIds(new Set())} placeholder={t(($) => $.permissions.task_permission_select_departments)} selectedLabel={t(($) => $.permissions.task_permission_organizations_selected)} selectAllLabel={t(($) => $.permissions.select_all)} clearLabel={t(($) => $.permissions.clear_selection)} noResultsLabel={t(($) => $.permissions.no_organizations)} loadingLabel={t(($) => $.permissions.loading)} errorLabel={t(($) => $.permissions.no_organizations)} removeLabel={t(($) => $.permissions.task_permission_remove_selected)} isLoading={directoryQuery.isLoading} hasError={directoryQuery.isError} ariaLabel={t(($) => $.permissions.task_permission_select_departments)} /></div> : <div className="flex min-h-9 flex-1 items-center rounded-md border px-3 text-caption text-muted-foreground">{t(($) => $.permissions.current_workspace_everyone)}</div>}
            <Select modal={false} items={availableRoles} value={role} onValueChange={(value) => setRole(value || "member")}><SelectTrigger className="w-full sm:w-44" aria-label={t(($) => $.permissions.task_role)}><SelectValue /></SelectTrigger><SelectContent alignItemWithTrigger={false}>{availableRoles.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
            <Button className="shrink-0" onClick={() => void createGrant()} disabled={saving || !canCreate}>{saving ? t(($) => $.permissions.saving) : t(($) => $.permissions.add_authorization)}</Button>
          </div>
        </section>
        <DialogFooter><Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.close)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </>;
}
