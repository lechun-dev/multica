"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import {
  dingtalkCallbackProtocol,
  isDesktopDingTalkState,
  useAuthStore,
} from "@multica/core/auth";
import { api } from "@multica/core/api";
import { workspaceKeys, workspaceListOptions } from "@multica/core/workspace/queries";
import { paths, resolvePostAuthDestination } from "@multica/core/paths";
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2 } from "lucide-react";
import { useT } from "@multica/views/i18n";

function CallbackContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const qc = useQueryClient();
  const { t } = useT("auth");
  const loginWithDingTalk = useAuthStore((s) => s.loginWithDingTalk);
  const [error, setError] = useState("");
  const [desktopToken, setDesktopToken] = useState<string | null>(null);

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

    if (isDesktopDingTalkState(state)) {
      const protocol = dingtalkCallbackProtocol(state);
      api
        .dingTalkLogin(code, state)
        .then(({ token }) => {
          setDesktopToken(token);
          window.location.href = `${protocol}://auth/callback?token=${encodeURIComponent(token)}`;
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : "DingTalk login failed");
        });
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

  if (desktopToken) {
    const state = searchParams.get("state") || "";
    const protocol = dingtalkCallbackProtocol(state);
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
            <CardTitle>{t(($) => $.web.desktop_handoff.opening_title)}</CardTitle>
            <CardDescription>
              {t(($) => $.web.desktop_handoff.opening_description)}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex justify-center">
            <Button
              variant="outline"
              onClick={() => {
                window.location.href = `${protocol}://auth/callback?token=${encodeURIComponent(desktopToken)}`;
              }}
            >
              {t(($) => $.web.desktop_handoff.open_button)}
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
            <CardTitle>{t(($) => $.web.dingtalk_callback.failed_title)}</CardTitle>
            <CardDescription>{error}</CardDescription>
          </CardHeader>
          <CardContent className="text-center">
            <a href={paths.login()} className="text-primary underline-offset-4 hover:underline">
              {t(($) => $.web.dingtalk_callback.back_to_login)}
            </a>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle>{t(($) => $.web.dingtalk_callback.completing_title)}</CardTitle>
          <CardDescription>{t(($) => $.web.dingtalk_callback.completing_description)}</CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center"><Loader2 className="h-6 w-6 animate-spin text-muted-foreground" /></CardContent>
      </Card>
    </div>
  );
}

export default function DingTalkCallbackPage() {
  return <Suspense fallback={null}><CallbackContent /></Suspense>;
}
