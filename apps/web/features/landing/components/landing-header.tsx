"use client";

import Link from "next/link";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { cn } from "@multica/ui/lib/utils";
import { useAuthStore } from "@multica/core/auth";
import { useLocale } from "../i18n";
import { useDashboardCtaHref } from "../utils/use-dashboard-cta";
import { headerButtonClassName } from "./shared";
import { PRODUCT_NAME } from "@/config/product-brand";

export function LandingHeader({
  variant = "dark",
}: {
  variant?: "dark" | "light";
}) {
  const { t } = useLocale();
  const user = useAuthStore((s) => s.user);
  const ctaHref = useDashboardCtaHref();
  const ctaLabel = user ? t.header.dashboard : t.header.cta;

  return (
    <header
      className={cn(
        "relative inset-x-0 top-0 z-30",
        variant === "dark"
          ? "absolute bg-transparent"
          : "border-b border-[#0a0d12]/8 bg-white",
      )}
    >
      <div className="mx-auto flex h-[76px] max-w-[1320px] items-center justify-between px-4 sm:px-6 lg:px-8">
        <div className="flex min-w-0 items-center">
          <Link href="/" className="flex shrink-0 items-center gap-3">
            <MulticaIcon
              className={cn(
                "size-5",
                variant === "dark" ? "text-white" : "text-[#0a0d12]",
              )}
              noSpin
            />
            <span
              className={cn(
                "text-title font-semibold tracking-[0.04em] sm:text-title-lg",
                variant === "dark" ? "text-white/92" : "text-[#0a0d12]",
              )}
            >
              {PRODUCT_NAME}
            </span>
          </Link>

        </div>

        <div className="flex shrink-0 items-center gap-2 sm:gap-2.5">
          <Link
            href={ctaHref}
            className={headerButtonClassName("solid", variant)}
          >
            {ctaLabel}
          </Link>
        </div>
      </div>

    </header>
  );
}
