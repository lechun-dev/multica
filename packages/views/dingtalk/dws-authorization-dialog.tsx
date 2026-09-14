"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link2, LoaderCircle, ShieldAlert } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import type { DingTalkOAuthClient } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import {
  beginDingTalkDWSAuthorizationAttempt,
  clearDingTalkDWSAuthorizationAttempt,
  dingTalkDWSAuthorizationTimeoutMs,
  readDingTalkDWSAuthorizationAttempt,
} from "./dws-authorization-attempt";

export const dingtalkDWSStatusKey = ["me", "dingtalk-dws"] as const;

export function DingTalkDWSAuthorizationDialog({
  client,
  openAuthorization,
}: {
  client: DingTalkOAuthClient;
  openAuthorization: (url: string) => void | Promise<void>;
}) {
  const { t } = useT("common");
  const authStatus = useAuthStore((state) => state.status);
  const [dismissed, setDismissed] = useState<string | null>(null);
  const [authorizationStartedAt, setAuthorizationStartedAt] = useState<
    number | null
  >(null);
  const [error, setError] = useState<string | null>(null);
  const authorizing = authorizationStartedAt !== null;
  const { data: status } = useQuery({
    queryKey: dingtalkDWSStatusKey,
    queryFn: () => api.getDingTalkDWSStatus(),
    enabled: authStatus === "authenticated",
    refetchInterval: authorizing ? 2_000 : 15_000,
    refetchIntervalInBackground: authorizing,
    refetchOnWindowFocus: true,
  });

  const finishAuthorizationAttempt = useCallback(() => {
    clearDingTalkDWSAuthorizationAttempt();
    setAuthorizationStartedAt(null);
  }, []);

  useEffect(() => {
    const startedAt = readDingTalkDWSAuthorizationAttempt();
    if (startedAt !== null) setAuthorizationStartedAt(startedAt);
  }, []);

  useEffect(() => {
    if (authorizationStartedAt === null) return;
    const remaining =
      dingTalkDWSAuthorizationTimeoutMs -
      (Date.now() - authorizationStartedAt);
    const handleTimeout = () => {
      finishAuthorizationAttempt();
      setError(t(($) => $.desktop.dws_auth.authorization_timeout));
    };
    if (remaining <= 0) {
      handleTimeout();
      return;
    }
    const timer = window.setTimeout(handleTimeout, remaining);
    return () => window.clearTimeout(timer);
  }, [authorizationStartedAt, finishAuthorizationAttempt, t]);

  const signature = useMemo(() => {
    if (!status) {
      return null;
    }
    if (
      status.state === "authorization_required" &&
      status.pending_count < 1
    )
      return null;
    if (status.state === "unavailable" && status.pending_count < 1)
      return null;
    if (
      !["authorization_required", "unavailable", "delivery_error"].includes(
        status.state,
      )
    )
      return null;
    return `${status.reason ?? "not_authorized"}:${status.pending_count}`;
  }, [status]);

  useEffect(() => {
    if (status && !signature) {
      setDismissed(null);
      finishAuthorizationAttempt();
      setError(null);
    }
  }, [finishAuthorizationAttempt, signature, status]);

  useEffect(() => {
    if (
      authorizing &&
      status &&
      status.state !== "authorization_required" &&
      status.state !== "connected"
    ) {
      finishAuthorizationAttempt();
    }
  }, [authorizing, finishAuthorizationAttempt, status]);

  const open = signature !== null && signature !== dismissed;
  const expired =
    status?.reason === "authorization_expired" ||
    status?.reason === "refresh_token_expired";
  const needsAuthorization = status?.state === "authorization_required";
  const title =
    status?.state === "unavailable"
      ? t(($) => $.desktop.dws_auth.unavailable_title)
      : status?.state === "delivery_error"
        ? t(($) => $.desktop.dws_auth.delivery_error_title)
        : expired
          ? t(($) => $.desktop.dws_auth.expired_title)
          : t(($) => $.desktop.dws_auth.not_logged_in_title);
  const description = needsAuthorization
    ? expired
      ? t(($) => $.desktop.dws_auth.expired_description)
      : t(($) => $.desktop.dws_auth.description)
    : status?.message || t(($) => $.desktop.dws_auth.delivery_error_description);

  const handleAuthorize = async () => {
    if (authorizing) return;
    const startedAt = beginDingTalkDWSAuthorizationAttempt();
    setAuthorizationStartedAt(startedAt);
    setError(null);
    try {
      const next =
        typeof window === "undefined"
          ? undefined
          : `${window.location.pathname}${window.location.search}${window.location.hash}`;
      const result = await api.startDingTalkDWSAuthorization(client, next);
      await openAuthorization(result.authorization_url);
    } catch (authorizationError) {
      finishAuthorizationAttempt();
      setError(
        authorizationError instanceof Error && authorizationError.message
          ? authorizationError.message
          : t(($) => $.desktop.dws_auth.failed),
      );
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen && signature && !authorizing) {
          setDismissed(signature);
          setError(null);
        }
      }}
    >
      <DialogContent showCloseButton={!authorizing} className="sm:max-w-md">
        <DialogHeader>
          <div className="flex items-start gap-3 pr-6">
            <div className="rounded-lg bg-brand/10 p-2 text-brand">
              {expired ? (
                <ShieldAlert className="size-5" />
              ) : (
                <Link2 className="size-5" />
              )}
            </div>
            <div className="min-w-0 space-y-2">
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>{description}</DialogDescription>
            </div>
          </div>
        </DialogHeader>

        {needsAuthorization ? (
          <p className="rounded-lg bg-surface-hover px-3 py-2 text-caption text-muted-foreground">
            {t(($) => $.desktop.dws_auth.server_hint)}
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-body text-destructive">
            {error}
          </p>
        ) : null}

        <DialogFooter>
          <Button
            variant="outline"
            disabled={authorizing}
            onClick={() => signature && setDismissed(signature)}
          >
            {t(($) => $.desktop.dws_auth.later)}
          </Button>
          {needsAuthorization ? (
            <Button disabled={authorizing} onClick={() => void handleAuthorize()}>
              {authorizing ? <LoaderCircle className="animate-spin" /> : null}
              {authorizing
                ? t(($) => $.desktop.dws_auth.authorizing)
                : expired
                  ? t(($) => $.desktop.dws_auth.reauthorize)
                  : t(($) => $.desktop.dws_auth.login)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
