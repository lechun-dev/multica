"use client";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Shield, UserPlus, UserMinus } from "lucide-react";
import { api } from "@multica/core/api";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { useState } from "react";

// 2026-08-27 coder(lq): Isolate task-level grants from the upstream issue detail UI.
export function IssuePermissionsPanel({ issueId, projectId }: { issueId: string; projectId?: string | null }) {
  const wsId = useWorkspaceId(); const qc = useQueryClient(); const [userId, setUserId] = useState(""); const [permission, setPermission] = useState("project.edit");
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data } = useQuery({ queryKey: ["issue-permissions", issueId], queryFn: () => api.listIssuePermissions(issueId), enabled: !!projectId });
  if (!projectId) return null;
  const grants = data?.permissions ?? []; const granted = new Set(grants.map((g) => `${g.user_id}:${g.permission}`));
  const memberItems = members.map((m) => ({ value: m.user_id, label: m.name }));
  const permissionValues = ["project.edit", "project.issue.manage", "project.agent.use"];
  const permissionItems = permissionValues.map((value) => ({ value, label: value }));
  const refresh = () => qc.invalidateQueries({ queryKey: ["issue-permissions", issueId] });
  const add = async () => { if (!userId) return; try { await api.grantIssuePermission(issueId, { user_id: userId, permission }); setUserId(""); refresh(); toast.success("任务权限已添加"); } catch (e) { toast.error(e instanceof Error ? e.message : "授权失败"); } };
  const remove = async (g: { user_id: string; permission: string }) => { try { await api.revokeIssuePermission(issueId, g.user_id, g.permission); refresh(); toast.success("任务权限已撤销"); } catch (e) { toast.error(e instanceof Error ? e.message : "撤销失败"); } };
  return <div className="rounded-lg border p-3 space-y-3"><div className="flex items-center gap-2 text-body font-medium"><Shield className="size-4" />任务权限（需先拥有项目查看权限）</div><div className="flex gap-2"><Select items={memberItems} value={userId} onValueChange={(v) => setUserId(v ?? "")}><SelectTrigger className="flex-1"><SelectValue placeholder="选择成员" /></SelectTrigger><SelectContent>{members.filter((m) => !granted.has(`${m.user_id}:${permission}`)).map((m) => <SelectItem key={m.user_id} value={m.user_id}>{m.name}</SelectItem>)}</SelectContent></Select><Select items={permissionItems} value={permission} onValueChange={(v) => setPermission(v ?? "project.edit")}><SelectTrigger className="w-36"><SelectValue /></SelectTrigger><SelectContent>{permissionValues.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}</SelectContent></Select><Button size="icon" onClick={add} disabled={!userId}><UserPlus className="size-4" /></Button></div>{grants.map((g) => <div key={`${g.user_id}:${g.permission}`} className="flex items-center gap-2 text-body"><span className="flex-1">{members.find((m) => m.user_id === g.user_id)?.name ?? g.user_id}</span><span className="text-caption text-muted-foreground">{g.permission}</span><Button variant="ghost" size="icon" onClick={() => remove(g)}><UserMinus className="size-4" /></Button></div>)}</div>;
}
