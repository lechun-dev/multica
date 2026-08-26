"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { workspaceKeys, workspaceListOptions } from "@multica/core/workspace/queries";
import { paths, resolvePostAuthDestination } from "@multica/core/paths";
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from "@multica/ui/components/ui/card";
import { Loader2 } from "lucide-react";

function CallbackContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const qc = useQueryClient();
  const loginWithDingTalk = useAuthStore((s) => s.loginWithDingTalk);
  const [error, setError] = useState("");

  useEffect(() => {
    const code = searchParams.get("code");
    const state = searchParams.get("state") || "";
    const providerError = searchParams.get("error");
    if (providerError) {
      setError(providerError === "access_denied" ? "Access denied" : providerError);
      return;
    }
    if (!code || !state) {
      setError("Missing DingTalk authorization code or state");
      return;
    }

    loginWithDingTalk(code, state)
      .then(async (user) => {
        const workspaces = await qc.ensureQueryData(workspaceListOptions());
        qc.setQueryData(workspaceKeys.list(), workspaces);
        router.replace(resolvePostAuthDestination(workspaces, user.onboarded_at != null));
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : "DingTalk login failed");
      });
  }, [loginWithDingTalk, qc, router, searchParams]);

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
            <CardTitle>钉钉登录失败</CardTitle>
            <CardDescription>{error}</CardDescription>
          </CardHeader>
          <CardContent className="text-center">
            <a href={paths.login()} className="text-primary underline-offset-4 hover:underline">返回登录</a>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle>正在完成钉钉登录</CardTitle>
          <CardDescription>请稍候…</CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center"><Loader2 className="h-6 w-6 animate-spin text-muted-foreground" /></CardContent>
      </Card>
    </div>
  );
}

export default function DingTalkCallbackPage() {
  return <Suspense fallback={null}><CallbackContent /></Suspense>;
}
