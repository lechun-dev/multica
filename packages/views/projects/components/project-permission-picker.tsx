"use client";

import { useMemo, useState } from "react";
import { Check, Search, ShieldCheck, UserMinus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { MemberWithUser, ProjectPermissionRole } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { PillButton } from "../../common/pill-button";
import { useT } from "../../i18n";

export interface ProjectPermissionSelection {
  userId: string;
  role: string;
}

const BUILTIN_PROJECT_ROLES: Array<{ key: string; labelKey: "role_owner" | "role_manager" | "role_member" | "role_viewer" }> = [
  { key: "owner", labelKey: "role_owner" },
  { key: "manager", labelKey: "role_manager" },
  { key: "member", labelKey: "role_member" },
  { key: "viewer", labelKey: "role_viewer" },
];

type ProjectPermissionPickerProps = {
  members: MemberWithUser[];
  workspaceId: string;
  value: ProjectPermissionSelection[];
  onChange: (value: ProjectPermissionSelection[]) => void;
};

// 2026-08-28 coder(lq): Keep creation-time authorization local until the
// project exists; the parent persists these selections through the existing
// project-member API after the create request succeeds.
export function ProjectPermissionPicker({
  members,
  workspaceId,
  value,
  onChange,
}: ProjectPermissionPickerProps) {
  const { t } = useT("projects");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [pendingIds, setPendingIds] = useState<Set<string>>(new Set());
  const [selectedRole, setSelectedRole] = useState("member");

  const rolesQuery = useQuery({
    queryKey: ["project-permission-roles", workspaceId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: open && !!workspaceId,
  });
  const roles = useMemo(() => {
    const persisted = rolesQuery.data?.roles ?? [];
    const persistedKeys = new Set(persisted.map((role) => role.key));
    const fallbackRoles: ProjectPermissionRole[] = BUILTIN_PROJECT_ROLES
      .filter(({ key }) => !persistedKeys.has(key))
      .map(({ key }) => ({
        id: `system-${workspaceId}-${key}`,
        workspace_id: workspaceId,
        key,
        // An empty name lets the localized label map render built-in roles;
        // custom roles from the server still provide their configured name.
        name: "",
        description: "",
        is_system: true,
        permissions: [],
      }));
    return [...persisted, ...fallbackRoles];
  }, [rolesQuery.data?.roles, workspaceId]);

  const roleLabels = useMemo(
    () => new Map(BUILTIN_PROJECT_ROLES.map(({ key, labelKey }) => [key, t(($) => $.permissions[labelKey])])),
    [t],
  );
  const roleLabel = (role: ProjectPermissionRole) => role.name || roleLabels.get(role.key) || role.key;
  const roleByKey = useMemo(() => new Map(roles.map((role) => [role.key, role])), [roles]);
  const selectedByUser = useMemo(() => new Map(value.map((item) => [item.userId, item])), [value]);
  const filteredMembers = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    return members
      .filter((member) => !selectedByUser.has(member.user_id))
      .filter((member) => {
        if (!needle) return true;
        return `${member.name} ${member.email}`.toLocaleLowerCase().includes(needle);
      })
      .sort((left, right) => left.name.localeCompare(right.name));
  }, [members, search, selectedByUser]);

  const togglePending = (userId: string) => {
    setPendingIds((current) => {
      const next = new Set(current);
      if (next.has(userId)) next.delete(userId);
      else next.add(userId);
      return next;
    });
  };

  const setPending = (userId: string, checked: boolean) => {
    setPendingIds((current) => {
      const next = new Set(current);
      if (checked) next.add(userId);
      else next.delete(userId);
      return next;
    });
  };

  const addPending = () => {
    if (pendingIds.size === 0) return;
    const additions = [...pendingIds].map((userId) => ({ userId, role: selectedRole }));
    onChange([...value, ...additions]);
    setPendingIds(new Set());
  };

  const removeSelection = (userId: string) => {
    onChange(value.filter((item) => item.userId !== userId));
  };

  const resetTransientState = () => {
    setSearch("");
    setPendingIds(new Set());
    setSelectedRole("member");
  };

  return (
    <Popover
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) resetTransientState();
      }}
    >
      <PopoverTrigger
        render={
          <PillButton aria-label={t(($) => $.permissions.authorize)}>
            <ShieldCheck className="size-3.5" />
            <span>
              {t(($) => $.permissions.authorize)}
              {value.length > 0 ? ` (${value.length})` : ""}
            </span>
          </PillButton>
        }
      />
      <PopoverContent side="top" align="start" className="w-80 space-y-3 p-3">
        <div>
          <div className="text-body font-medium">{t(($) => $.permissions.dialog_title)}</div>
          <p className="text-caption text-muted-foreground">{t(($) => $.permissions.dialog_description)}</p>
        </div>

        {value.length > 0 && (
          <div className="space-y-1 border-b pb-2">
            <div className="text-caption font-medium text-muted-foreground">{t(($) => $.permissions.current_access)}</div>
            {value.map((selection) => {
              const member = members.find((item) => item.user_id === selection.userId);
              const name = member?.name || member?.email || selection.userId;
              const role = roleByKey.get(selection.role);
              return (
                <div key={selection.userId} className="flex items-center gap-2 text-caption">
                  <span className="min-w-0 flex-1 truncate">{name}</span>
                  <span className="text-muted-foreground">{role ? roleLabel(role) : selection.role}</span>
                  <button
                    type="button"
                    className="text-muted-foreground hover:text-foreground"
                    aria-label={`${t(($) => $.permissions.remove_aria)} ${name}`}
                    onClick={() => removeSelection(selection.userId)}
                  >
                    <UserMinus className="size-3" />
                  </button>
                </div>
              );
            })}
          </div>
        )}

        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t(($) => $.permissions.search_placeholder)}
            aria-label={t(($) => $.permissions.search_placeholder)}
            className="h-8 w-full rounded-md border bg-transparent pl-7 pr-2 text-caption outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
          />
        </div>

        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button type="button" variant="outline" size="sm" className="min-w-28 justify-between text-caption">
                  {roleByKey.get(selectedRole) ? roleLabel(roleByKey.get(selectedRole)!) : roleLabels.get(selectedRole) || selectedRole}
                </Button>
              }
            />
            <DropdownMenuContent align="start">
              {roles.map((role) => (
                <DropdownMenuItem key={role.key} onClick={() => setSelectedRole(role.key)}>
                  {roleLabel(role)}
                  {role.key === selectedRole && <Check className="ml-auto size-3.5" />}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <Button type="button" size="sm" onClick={addPending} disabled={pendingIds.size === 0}>
            {t(($) => $.permissions.add_members)}
          </Button>
        </div>

        <div className="max-h-44 space-y-1 overflow-y-auto">
          {filteredMembers.length === 0 ? (
            <div className="py-4 text-center text-caption text-muted-foreground">{t(($) => $.permissions.no_results)}</div>
          ) : (
            filteredMembers.map((member) => (
              <div
                key={member.user_id}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-caption hover:bg-accent"
                onClick={(event) => {
                  if ((event.target as HTMLElement).closest('[data-slot="checkbox"]')) return;
                  togglePending(member.user_id);
                }}
              >
                <Checkbox
                  checked={pendingIds.has(member.user_id)}
                  aria-label={member.name || member.email}
                  onCheckedChange={(nextChecked) =>
                    setPending(member.user_id, nextChecked === true)
                  }
                />
                <span className="min-w-0 flex-1 truncate">{member.name || member.email}</span>
                {member.name && member.email && <span className="max-w-32 truncate text-muted-foreground">{member.email}</span>}
              </div>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
