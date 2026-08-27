"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { UserMinus, UserPlus, Shield } from "lucide-react";
import { api } from "@multica/core/api";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { useState } from "react";

// 2026-08-27 coder(lq): Keep project authorization UI isolated from the upstream project detail surface.
export function ProjectPermissionsPanel({ projectId, canManage }: { projectId: string; canManage: boolean }) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: workspaceMembers = [] } = useQuery(memberListOptions(wsId));
  const { data, isLoading } = useQuery({ queryKey: ["project-members", projectId], queryFn: () => api.listProjectMembers(projectId) });
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState("member");
  const assigned = new Set((data?.members ?? []).map((m) => m.user_id));
  const refresh = () => qc.invalidateQueries({ queryKey: ["project-members", projectId] });
  const add = async () => { if (!userId) return; try { await api.addProjectMember(projectId, { user_id: userId, role }); setUserId(""); refresh(); toast.success("项目成员已添加"); } catch (e) { toast.error(e instanceof Error ? e.message : "添加失败"); } };
  const remove = async (id: string) => { try { await api.removeProjectMember(projectId, id); refresh(); toast.success("项目成员已移除"); } catch (e) { toast.error(e instanceof Error ? e.message : "移除失败"); } };
  return <div className="rounded-lg border p-3 space-y-3">
    <div className="flex items-center gap-2 text-sm font-medium"><Shield className="size-4" />项目权限</div>
    {canManage && <div className="flex gap-2"><Select value={userId} onValueChange={(v) => setUserId(v ?? "")}><SelectTrigger className="flex-1"><SelectValue placeholder="选择工作区成员" /></SelectTrigger><SelectContent>{workspaceMembers.filter((m) => !assigned.has(m.user_id)).map((m) => <SelectItem key={m.user_id} value={m.user_id}>{m.name}</SelectItem>)}</SelectContent></Select><Select value={role} onValueChange={(v) => setRole(v ?? "member")}><SelectTrigger className="w-28"><SelectValue /></SelectTrigger><SelectContent>{["owner", "manager", "member", "viewer"].map((r) => <SelectItem key={r} value={r}>{r}</SelectItem>)}</SelectContent></Select><Button size="icon" onClick={add} disabled={!userId}><UserPlus className="size-4" /></Button></div>}
    {isLoading ? <div className="text-xs text-muted-foreground">加载中…</div> : (data?.members ?? []).map((m) => <div key={m.user_id} className="flex items-center gap-2 text-sm"><span className="flex-1">{workspaceMembers.find((w) => w.user_id === m.user_id)?.name ?? m.user_id}</span><span className="text-xs text-muted-foreground">{m.role}</span>{canManage && <Button variant="ghost" size="icon" onClick={() => remove(m.user_id)}><UserMinus className="size-4" /></Button>}</div>)}
  </div>;
}
