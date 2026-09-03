"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck, UserMinus } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { ProjectAccessGrantSubjectType } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { ProjectPermissionOrganizationSelect } from "../../projects/components/project-permission-organization-select";

type IssueAccessGrantsDialogProps = {
  issueId: string;
  projectId: string;
};

/**
 * Task authorization deliberately has its own dialog. Project grants are
 * read-only context here; writes always include this issue ID and can only
 * use the task-safe permission subset enforced by the backend.
 * 2026-08-31 coder(lq): Keep task direct grants separate from project member UI.
 */
export function IssueAccessGrantsDialog({ issueId, projectId }: IssueAccessGrantsDialogProps) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [subjectType, setSubjectType] = useState<ProjectAccessGrantSubjectType>("user");
  const [subjectId, setSubjectId] = useState("");
  const [selectedUser, setSelectedUser] = useState("");
  const [grantKind, setGrantKind] = useState<"role" | "permission">("permission");
  const [role, setRole] = useState("member");
  const [permission, setPermission] = useState("project.view");
  const [saving, setSaving] = useState(false);

  const grantsQuery = useQuery({
    queryKey: ["issue-access-grants", workspaceId, issueId, projectId],
    queryFn: () => api.listIssueAccessGrants(issueId),
    enabled: open,
  });
  const rolesQuery = useQuery({
    queryKey: ["project-permission-roles", workspaceId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: open && !!workspaceId,
  });
  const directoryQuery = useQuery({
    queryKey: ["project-permission-organizations", workspaceId],
    queryFn: () => api.listProjectAuthorizationOrganizations(workspaceId),
    enabled: open && !!workspaceId,
    staleTime: 60_000,
  });
  const { data: members = [] } = useQuery({
    ...memberListOptions(workspaceId),
    enabled: open && !!workspaceId,
  });
  const memberByUser = useMemo(() => new Map(members.map((member) => [member.user_id, member])), [members]);
  const organizationById = useMemo(
    () => new Map((directoryQuery.data?.organizations ?? []).map((organization) => [organization.id, organization])),
    [directoryQuery.data?.organizations],
  );
  const roleByKey = useMemo(
    () => new Map((rolesQuery.data?.roles ?? []).map((item) => [item.key, item])),
    [rolesQuery.data?.roles],
  );
  const grants = grantsQuery.data?.grants ?? [];
  const directGrants = useMemo(() => grants.filter((grant) => grant.issue_id === issueId), [grants, issueId]);
  const inheritedGrants = useMemo(() => grants.filter((grant) => grant.issue_id !== issueId), [grants, issueId]);
  const taskRoles = useMemo(() => [
    { value: "viewer", label: t(($) => $.permissions.role_viewer) },
    { value: "member", label: t(($) => $.permissions.role_member) },
    { value: "manager", label: t(($) => $.permissions.role_manager) },
  ], [t]);
  const roleSubjects = useMemo(
    () => (rolesQuery.data?.roles?.length ? rolesQuery.data.roles.map((item) => ({ value: item.key, label: item.name || item.key })) : taskRoles),
    [rolesQuery.data?.roles, taskRoles],
  );
  const taskPermissions = useMemo(() => [
    { value: "project.view", label: t(($) => $.permissions.view_task) },
    { value: "project.edit", label: t(($) => $.permissions.edit_task) },
    { value: "project.issue.comment", label: t(($) => $.permissions.comment_task) },
    { value: "project.issue.manage", label: t(($) => $.permissions.manage_task) },
    { value: "project.issue.archive", label: t(($) => $.permissions.archive_task) },
    { value: "project.agent.use", label: t(($) => $.permissions.use_agent) },
  ], [t]);
  const subjectTypes = useMemo<Array<{ value: ProjectAccessGrantSubjectType; label: string }>>(() => [
    { value: "user", label: t(($) => $.permissions.user) },
    { value: "organization", label: t(($) => $.permissions.organization) },
    { value: "role", label: t(($) => $.permissions.role) },
    { value: "everyone", label: t(($) => $.permissions.everyone) },
  ], [t]);

  const reset = () => {
    setSubjectType("user");
    setSubjectId("");
    setSelectedUser("");
    setGrantKind("permission");
    setRole("member");
    setPermission("project.view");
  };

  const createGrant = async () => {
    const resolvedSubjectId = subjectType === "user" ? selectedUser : subjectType === "everyone" ? undefined : subjectId.trim();
    if (subjectType !== "everyone" && !resolvedSubjectId) {
      toast.error(t(($) => $.permissions.task_authorization_subject_required));
      return;
    }
    setSaving(true);
    try {
      // 2026-09-01 coder(lq): A role subject means "members of this project
      // role" and therefore always carries a single task-safe permission;
      // sending a role here would create an ambiguous role-to-role grant.
      const effectiveGrantKind = subjectType === "role" ? "permission" : grantKind;
      await api.createIssueAccessGrant(issueId, {
        subject_type: subjectType,
        subject_id: resolvedSubjectId,
        ...(effectiveGrantKind === "role" ? { role } : { permission }),
      });
      await queryClient.invalidateQueries({ queryKey: ["issue-access-grants", workspaceId, issueId, projectId] });
      reset();
      toast.success(t(($) => $.permissions.task_authorization_added));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.task_authorization_add_failed));
    } finally {
      setSaving(false);
    }
  };

  const revoke = async (grant: (typeof grants)[number]) => {
    try {
      await api.revokeIssueAccessGrant(issueId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: grant.role,
        permission: grant.permission,
      });
      await queryClient.invalidateQueries({ queryKey: ["issue-access-grants", workspaceId, issueId, projectId] });
      toast.success(t(($) => $.permissions.task_authorization_removed));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.task_authorization_remove_failed));
    }
  };

  const renderGrant = (grant: (typeof grants)[number], inherited: boolean) => {
    const subjectLabel = subjectTypes.find((item) => item.value === grant.subject_type)?.label || grant.subject_type;
    const subjectName = grant.subject_type === "everyone"
      ? t(($) => $.permissions.current_workspace_everyone)
      : grant.subject_type === "user"
        ? memberByUser.get(grant.subject_id || "")?.name || memberByUser.get(grant.subject_id || "")?.email || grant.subject_id || ""
        : grant.subject_type === "organization"
          ? organizationById.get(grant.subject_id || "")?.name || grant.subject_id || ""
          : roleByKey.get(grant.subject_id || "")?.name || grant.subject_id || "";
    const label = grant.subject_type === "everyone"
      ? subjectName
      : `${subjectLabel}: ${subjectName}`;
    const permissionLabel = grant.permission
      ? taskPermissions.find((item) => item.value === grant.permission)?.label || grant.permission
      : grant.role
        ? roleByKey.get(grant.role)?.name || taskRoles.find((item) => item.value === grant.role)?.label || grant.role
        : grant.source;
    const sourceLabel = inherited
      ? t(($) => $.permissions.inherited_from_project)
      : t(($) => $.permissions.direct_task_grant);
    return (
      <div key={`${grant.id}-${grant.issue_id || "project"}-${grant.subject_type}-${grant.subject_id}-${grant.role}-${grant.permission}`} className="flex items-center gap-3 rounded-lg border px-2 py-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-body font-medium">{label}</div>
          <div className="truncate text-caption text-muted-foreground">{permissionLabel} · {sourceLabel}</div>
        </div>
        {!inherited && <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_task_access_aria)} ${label}`} onClick={() => void revoke(grant)}><UserMinus className="size-3.5" /></Button>}
      </div>
    );
  };

  return (
    <>
      <Button variant="ghost" size="icon-sm" className="text-muted-foreground" onClick={() => setOpen(true)} aria-label={t(($) => $.permissions.task_permissions_title)}>
        <ShieldCheck />
      </Button>
      <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (!next) reset(); }}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t(($) => $.permissions.task_permissions_title)}</DialogTitle>
            <DialogDescription>{t(($) => $.permissions.task_permissions_description)}</DialogDescription>
          </DialogHeader>

          <section className="space-y-2">
            <h3 className="text-body font-medium">{t(($) => $.permissions.inherited_access)}</h3>
            {inheritedGrants.length === 0 ? <p className="rounded-lg border border-dashed p-3 text-caption text-muted-foreground">{t(($) => $.permissions.no_inherited_access)}</p> : <div className="max-h-40 space-y-1 overflow-y-auto">{inheritedGrants.map((grant) => renderGrant(grant, true))}</div>}
          </section>
          <section className="space-y-2">
            <h3 className="text-body font-medium">{t(($) => $.permissions.direct_access)}</h3>
            {directGrants.length === 0 ? <p className="rounded-lg border border-dashed p-3 text-caption text-muted-foreground">{t(($) => $.permissions.no_direct_access)}</p> : <div className="max-h-40 space-y-1 overflow-y-auto">{directGrants.map((grant) => renderGrant(grant, false))}</div>}
          </section>

          <section className="space-y-2 border-t pt-3">
            <h3 className="text-body font-medium">{t(($) => $.permissions.add_task_access)}</h3>
            <div className="grid gap-2 sm:grid-cols-2">
              <Select items={subjectTypes} value={subjectType} onValueChange={(value) => setSubjectType((value as ProjectAccessGrantSubjectType) || "user")}><SelectTrigger aria-label={t(($) => $.permissions.authorization_subject)}><SelectValue /></SelectTrigger><SelectContent>{subjectTypes.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
              {subjectType === "user" ? <Select items={members.map((member) => ({ value: member.user_id, label: member.name || member.email || member.user_id }))} value={selectedUser} onValueChange={(value) => setSelectedUser(value || "")}><SelectTrigger aria-label={t(($) => $.permissions.select_user)}><SelectValue placeholder={t(($) => $.permissions.select_user)} /></SelectTrigger><SelectContent>{members.map((member) => <SelectItem key={member.user_id} value={member.user_id}>{member.name || member.email || member.user_id}</SelectItem>)}</SelectContent></Select> : subjectType === "everyone" ? <div className="rounded-md border px-3 py-2 text-caption text-muted-foreground">{t(($) => $.permissions.current_workspace_everyone)}</div> : subjectType === "organization" && workspaceId ? <ProjectPermissionOrganizationSelect workspaceId={workspaceId} open={open} value={subjectId} onValueChange={setSubjectId} ariaLabel={t(($) => $.permissions.organization)} placeholder={t(($) => $.permissions.select_organization)} emptyLabel={t(($) => $.permissions.no_organizations)} /> : <Select items={roleSubjects} value={subjectId} onValueChange={(value) => setSubjectId(value || "")}><SelectTrigger aria-label={t(($) => $.permissions.role_key)}><SelectValue placeholder={t(($) => $.permissions.select_project_role_key)} /></SelectTrigger><SelectContent>{roleSubjects.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>}
              {subjectType !== "role" && <Select items={[{ value: "permission", label: t(($) => $.permissions.single_permission) }, { value: "role", label: t(($) => $.permissions.role_permissions) }]} value={grantKind} onValueChange={(value) => setGrantKind((value as "role" | "permission") || "permission")}><SelectTrigger aria-label={t(($) => $.permissions.grant_type)}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="permission">{t(($) => $.permissions.single_permission)}</SelectItem><SelectItem value="role">{t(($) => $.permissions.role_permissions)}</SelectItem></SelectContent></Select>}
              {(subjectType !== "role" && grantKind === "role") ? <Select items={taskRoles} value={role} onValueChange={(value) => setRole(value || "member")}><SelectTrigger aria-label={t(($) => $.permissions.task_role)}><SelectValue /></SelectTrigger><SelectContent>{taskRoles.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select> : <Select items={taskPermissions} value={permission} onValueChange={(value) => setPermission(value || "project.view")}><SelectTrigger aria-label={t(($) => $.permissions.task_permission)}><SelectValue /></SelectTrigger><SelectContent>{taskPermissions.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>}
            </div>
          </section>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.close)}</Button>
            <Button onClick={() => void createGrant()} disabled={saving || (subjectType === "user" ? !selectedUser : subjectType !== "everyone" && !subjectId.trim())}>{saving ? t(($) => $.permissions.saving) : t(($) => $.permissions.add_authorization)}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
