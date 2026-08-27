"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow } from "./settings-layout";

const dingtalkProfileKey = ["me", "dingtalk-profile"] as const;

/** Read-only DingTalk identity details for the currently authenticated user. */
export function DingTalkProfileCard() {
  const { t } = useT("settings");
  const { data: profile, isLoading } = useQuery({
    queryKey: dingtalkProfileKey,
    queryFn: () => api.getDingTalkProfile(),
    staleTime: 60_000,
  });

  if (isLoading || !profile) return null;

  return (
    <SettingsCard>
      <SettingsRow
        label={t(($) => $.account.dingtalk_label)}
        description={t(($) => $.account.dingtalk_hint)}
        size="none"
      >
        <div className="flex items-center gap-3 sm:justify-end">
          <Avatar size="lg">
            {profile.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
            <AvatarFallback>{(profile.name || "钉").slice(0, 1)}</AvatarFallback>
          </Avatar>
          <div className="min-w-0 text-left sm:text-right">
            <p className="text-body font-medium">
              {profile.bound
                ? t(($) => $.account.dingtalk_connected)
                : t(($) => $.account.dingtalk_not_connected)}
            </p>
            {profile.bound ? (
              <p className="truncate text-caption text-muted-foreground">
                {profile.name || profile.email || t(($) => $.account.dingtalk_identity_unknown)}
              </p>
            ) : null}
          </div>
        </div>
      </SettingsRow>
      {profile.bound ? (
        <>
          {profile.email ? (
            <SettingsRow label={t(($) => $.account.dingtalk_email)} size="text">
              <p className="break-all text-caption text-muted-foreground sm:text-right">
                {profile.email}
              </p>
            </SettingsRow>
          ) : null}
        </>
      ) : null}
    </SettingsCard>
  );
}
