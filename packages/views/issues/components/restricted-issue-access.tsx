"use client";

/* eslint-disable i18next/no-literal-string -- Isolated restricted-task fallback copy ships with the access-request surface. */
/* eslint-disable no-restricted-syntax -- Toast fallback copy is local to this fail-closed route. */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";

export function RestrictedIssueAccess({ issueId, identifier }: { issueId: string; identifier: string }) {
  const [role, setRole] = useState("viewer");
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const roles = useQuery({ queryKey: ["task-permission-roles", "restricted"], queryFn: () => api.listTaskPermissionRoles() });
  const requests = useQuery({ queryKey: ["issue-access-requests", issueId, "mine"], queryFn: () => api.listIssueAccessRequests(issueId, true) });

  const submit = async () => {
    setSubmitting(true);
    try {
      await api.createIssueAccessRequest(issueId, {
        requested_role: role,
        reason: reason.trim() || undefined,
        idempotency_key: crypto.randomUUID(),
      });
      await requests.refetch();
      setReason("");
      toast.success("Access request submitted to the current task Owner");
    } catch { toast.error("Access request could not be submitted. Please retry."); }
    finally { setSubmitting(false); }
  };

  const cancel = async (requestId: string) => {
    try { await api.cancelIssueAccessRequest(issueId, requestId); await requests.refetch(); }
    catch { toast.error("Access request could not be cancelled. Please retry."); }
  };

  return <main className="mx-auto flex min-h-[60vh] w-full max-w-xl flex-col justify-center gap-5 p-6" aria-labelledby="restricted-task-title">
    <div><p className="text-caption text-muted-foreground">{identifier}</p><h1 id="restricted-task-title" className="text-xl font-semibold">This task is restricted</h1><p className="mt-2 text-body text-muted-foreground">Its content and sharing list are hidden. Request a complete task role from the current task Owner.</p></div>
    <div className="space-y-3 rounded-lg border p-4"><label className="block text-sm font-medium" htmlFor="access-reason">Requested task role</label><Select items={(roles.data?.roles ?? []).map((item) => ({ value: item.key, label: item.name }))} value={role} onValueChange={(value) => setRole(value || "viewer")}><SelectTrigger aria-label="Requested task role"><SelectValue /></SelectTrigger><SelectContent>{roles.data?.roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name}</SelectItem>)}</SelectContent></Select><label className="block text-sm font-medium" htmlFor="access-reason">Reason (optional)</label><textarea id="access-reason" className="min-h-24 w-full rounded-md border bg-background p-2 text-sm" maxLength={2000} value={reason} onChange={(event) => setReason(event.target.value)} /><Button onClick={() => void submit()} disabled={submitting || !role}>{submitting ? "Submitting…" : "Request access"}</Button></div>
    {requests.data?.items.length ? <section className="space-y-2"><h2 className="font-medium">My requests</h2>{requests.data.items.map((request) => <div key={request.id} className="flex items-center gap-2 rounded-md border p-3"><span className="flex-1"><code>task:{request.requested_role}</code> · {request.status}</span>{request.status === "pending" ? <Button variant="outline" size="sm" onClick={() => void cancel(request.id)}>Cancel</Button> : null}</div>)}</section> : null}
  </main>;
}
