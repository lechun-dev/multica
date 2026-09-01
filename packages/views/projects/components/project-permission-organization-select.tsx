"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { ProjectAuthorizationOrganization } from "@multica/core/types";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";

type OrganizationSelectProps = {
  workspaceId: string;
  open: boolean;
  value: string;
  onValueChange: (value: string) => void;
  ariaLabel: string;
  placeholder: string;
  emptyLabel: string;
};

export function ProjectPermissionOrganizationSelect({
  workspaceId,
  open,
  value,
  onValueChange,
  ariaLabel,
  placeholder,
  emptyLabel,
}: OrganizationSelectProps) {
  const query = useQuery({
    queryKey: ["project-permission-organizations", workspaceId],
    queryFn: () => api.listProjectAuthorizationOrganizations(workspaceId),
    enabled: open && !!workspaceId,
    staleTime: 60_000,
  });
  const organizations = query.data?.organizations ?? [];
  const items = organizations.map((organization: ProjectAuthorizationOrganization) => ({
    value: organization.id,
    label: organization.provider ? `${organization.name || organization.external_id} · ${organization.provider}` : (organization.name || organization.external_id),
  }));

  if (!query.isLoading && organizations.length === 0) {
    return <div className="rounded-md border px-3 py-2 text-caption text-muted-foreground">{emptyLabel}</div>;
  }

  return (
    <Select items={items} value={value} onValueChange={(next) => onValueChange(next || "")}>
      <SelectTrigger aria-label={ariaLabel}>
        <SelectValue placeholder={query.isLoading ? "…" : placeholder} />
      </SelectTrigger>
      <SelectContent>
        {organizations.map((organization) => (
          <SelectItem key={organization.id} value={organization.id}>
            {organization.name || organization.external_id}{organization.provider ? ` · ${organization.provider}` : ""}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
