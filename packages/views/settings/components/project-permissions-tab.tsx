"use client";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

// 2026-08-27 coder(lq): Matrix view keeps workspace-wide authorization review compact and read-only.
export function ProjectPermissionsTab() {
  const { data, isLoading, error } = useQuery({ queryKey: ["project-permission-report"], queryFn: () => api.listProjectPermissionReport() });
  const rows = data?.rows ?? [];
  const projects = [...new Map(rows.map((r) => [r.project_id, r.project_title])).entries()];
  const users = [...new Map(rows.map((r) => [r.user_id, { name: r.user_name, email: r.user_email }])).entries()];
  const cell = (userId: string, projectId: string) => { const grants = rows.filter((r) => r.user_id === userId && r.project_id === projectId); if (!grants.length) return "—"; const roles = [...new Set(grants.map((r) => r.project_role).filter(Boolean))]; const perms = [...new Set(grants.map((r) => r.permission).filter(Boolean))]; const issues = grants.filter((r) => r.scope === "issue").length; return <span title={perms.join(", ")}>{roles.join(", ") || "授权"}{issues ? ` · 任务${issues}` : ""}</span>; };
  return <SettingsTab title="项目权限" description="按人员和项目查看有效权限及任务级授权。"><SettingsSection><SettingsCard>{isLoading ? <p>加载中…</p> : error ? <p className="text-destructive">暂无权限报告访问权限</p> : <div className="overflow-auto"><table className="w-full text-sm"><thead><tr><th className="sticky left-0 bg-background p-2 text-left">人员 \ 项目</th>{projects.map(([id, title]) => <th key={id} className="min-w-36 p-2 text-left">{title}</th>)}</tr></thead><tbody>{users.map(([id, user]) => <tr key={id} className="border-t"><td className="sticky left-0 bg-background p-2"><div>{user.name}</div><div className="text-xs text-muted-foreground">{user.email}</div></td>{projects.map(([pid]) => <td key={pid} className="p-2 align-top">{cell(id, pid)}</td>)}</tr>)}</tbody></table></div>}</SettingsCard></SettingsSection></SettingsTab>;
}
