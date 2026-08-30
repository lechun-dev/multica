import type { Metadata } from "next";
import { ChangelogPageClient } from "@/features/landing/components/changelog-page-client";
import { PRODUCT_NAME } from "@/config/product-brand";

export const metadata: Metadata = {
  title: "Changelog",
  description:
    `See what's new in ${PRODUCT_NAME} — latest features, improvements, and fixes.`,
  openGraph: {
    title: `Changelog | ${PRODUCT_NAME}`,
    description: `Latest updates and releases from ${PRODUCT_NAME}.`,
    url: "/changelog",
  },
  alternates: {
    canonical: "/changelog",
  },
};

export default function ChangelogPage() {
  return <ChangelogPageClient />;
}
