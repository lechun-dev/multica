"use client";

import Image from "next/image";
import Link from "next/link";
import { Download } from "lucide-react";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { useLocale } from "../i18n";
import { heroButtonClassName } from "./shared";
import { PRODUCT_NAME } from "@/config/product-brand";

/**
 * Local app-entry landing page for the self-hosted Multica deployment.
 *
 * Kept separate from the upstream marketing landing components so pulling
 * future Multica source updates does not overwrite this page's customization.
 */
export function MulticaAppEntry() {
  const { t } = useLocale();

  return (
    <main className="relative isolate min-h-full overflow-hidden bg-[#05070b] text-white">
      <Image
        src="/images/landing-bg.webp"
        alt=""
        fill
        priority
        className="object-cover object-center"
        sizes="100vw"
      />
      <div className="absolute inset-0 bg-[#05070b]/18" aria-hidden />

      <div className="relative z-10 flex min-h-full flex-col">
        <header className="mx-auto flex h-[76px] w-full max-w-[1320px] items-center px-4 sm:px-6 lg:px-8">
          <Link href="/" className="flex items-center gap-3" aria-label={PRODUCT_NAME}>
            <MulticaIcon className="size-5 text-white" noSpin />
            <span className="text-title font-semibold tracking-[0.04em] text-white/92 sm:text-title-lg">
              {PRODUCT_NAME}
            </span>
          </Link>
        </header>

        <section className="mx-auto flex w-full max-w-[1320px] flex-1 items-center justify-center px-4 pb-16 pt-10 sm:px-6 sm:pb-24 sm:pt-14 lg:px-8">
          <div className="w-full max-w-[1120px] text-center">
            <h1 className="landing-serif text-[3.65rem] leading-[0.93] tracking-[-0.038em] text-white drop-shadow-[0_10px_34px_rgba(0,0,0,0.32)] sm:text-[4.85rem] lg:text-[6.4rem]">
              {t.hero.headlineLine1}
              <br />
              {t.hero.headlineLine2}
            </h1>

            <p className="mx-auto mt-7 max-w-[820px] text-body-lg leading-7 text-white/84 sm:text-title">
              {t.hero.subheading}
            </p>

            <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
              <Link href="/login" className={heroButtonClassName("solid")}>
                {t.hero.cta}
              </Link>
              <Link href="/download" className={heroButtonClassName("ghost")}>
                <Download className="size-4" aria-hidden />
                {t.hero.downloadDesktop}
              </Link>
            </div>
          </div>
        </section>
      </div>
    </main>
  );
}
