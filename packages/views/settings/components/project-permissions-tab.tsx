"use client";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { useT } from "../../i18n";

// 2026-08-27 coder(lq): Matrix view keeps workspace-wide authorization review compact and read-only.
export function ProjectPermissionsTab() {
  const { t } = useT("settings");
  const { data, isLoading, error } = useQuery({
    queryKey: ["project-permission-report"],
    queryFn: () => api.listProjectPermissionReport(),
  });
  const rows = data?.rows ?? [];
  const projects = [...new Map(rows.map((r) => [r.project_id, r.project_title])).entries()];
  const users = [...new Map(rows.map((r) => [r.user_id, { name: r.user_name, email: r.user_email }])).entries()];
  const cell = (userId: string, projectId: string) => {
    const grants = rows.filter((r) => r.user_id === userId && r.project_id === projectId);
    if (!grants.length) return "—";
    const roles = [...new Set(grants.map((r) => r.project_role).filter(Boolean))];
    const permissions = [...new Set(grants.map((r) => r.permission).filter(Boolean))];
    return (
      <span title={permissions.join(", ")}>
        {roles.join(", ") || t(($) => $.permission_report.granted)}
      </span>
    );
  };

  return (
    <SettingsTab
      title={t(($) => $.permission_report.title)}
      description={t(($) => $.permission_report.description)}
    >
      <SettingsSection>
        <SettingsCard>
          {isLoading ? (
            <p>{t(($) => $.permission_report.loading)}</p>
          ) : error ? (
            <p className="text-destructive">{t(($) => $.permission_report.forbidden)}</p>
          ) : (
            <div className="overflow-auto">
              <table className="w-full text-body">
                <thead>
                  <tr>
                    <th className="sticky left-0 bg-background p-2 text-left">
                      {t(($) => $.permission_report.people_projects)}
                    </th>
                    {projects.map(([id, title]) => (
                      <th key={id} className="min-w-36 p-2 text-left">{title}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {users.map(([id, user]) => (
                    <tr key={id} className="border-t">
                      <td className="sticky left-0 bg-background p-2">
                        <div>{user.name}</div>
                        <div className="text-caption text-muted-foreground">{user.email}</div>
                      </td>
                      {projects.map(([projectId]) => projectId ? (
                        <td key={projectId} className="p-2 align-top">
                          {cell(id ?? "", projectId)}
                        </td>
                      ) : null)}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
