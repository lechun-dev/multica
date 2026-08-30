"use client";

import { useEffect, useState } from "react";
import { LandingHeader } from "@/features/landing/components/landing-header";
import { DownloadHero } from "@/features/landing/components/download/hero";
import { AllPlatforms } from "@/features/landing/components/download/all-platforms";
import { useLocale } from "@/features/landing/i18n";
import {
  detectOS,
  type DetectResult,
} from "@/features/landing/utils/os-detect";
import type { LatestRelease } from "@/features/landing/utils/github-release";

export function DownloadClient({ release }: { release: LatestRelease }) {
  const [detected, setDetected] = useState<DetectResult | null>(null);
  const versionUnavailable = release.version === null;

  useEffect(() => {
    let cancelled = false;
    detectOS().then((result) => {
      if (cancelled) return;
      setDetected(result);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <>
      {/* Positioning context for the dark-variant LandingHeader —
          mirrors multica-landing.tsx. The header is `absolute top-0
          inset-x-0`, so it anchors to this `relative` wrapper and
          scrolls off together with the dark hero below. Without the
          wrapper, `absolute` would escape to the initial containing
          block and read as fixed. */}
      <div className="relative">
        <LandingHeader variant="dark" />
        <DownloadHero
          detected={detected}
          assets={release.assets}
          versionUnavailable={versionUnavailable}
        />
      </div>

      {/* 2026-08-30 coder(lq): Keep the private deployment download page
          focused on the three supported desktop platform builds. */}
      <AllPlatforms assets={release.assets} />
      <VersionInfoFooter version={release.version} />
    </>
  );
}

function VersionInfoFooter({ version }: { version: string | null }) {
  const { t } = useLocale();
  const d = t.download.footer;

  return (
    <section className="bg-white pb-16 text-[#0a0d12] sm:pb-20">
      <div className="mx-auto max-w-[920px] border-t border-[#0a0d12]/8 px-4 pt-8 text-label text-[#0a0d12]/60 sm:px-6 lg:px-8">
        {version
          ? d.currentVersion.replace("{version}", version)
          : d.versionUnavailable}
      </div>
    </section>
  );
}
