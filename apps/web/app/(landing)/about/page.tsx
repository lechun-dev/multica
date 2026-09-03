import type { Metadata } from "next";
import { AboutPageClient } from "@/features/landing/components/about-page-client";
import { PRODUCT_NAME } from "@/config/product-brand";

export const metadata: Metadata = {
  title: "About",
  description:
    `Learn about ${PRODUCT_NAME} — multiplexed information and computing agent. An open-source project management platform for human + agent teams.`,
  openGraph: {
    title: `About ${PRODUCT_NAME}`,
    description:
      `The story behind ${PRODUCT_NAME} and why we're building project management for human + agent teams.`,
    url: "/about",
  },
  alternates: {
    canonical: "/about",
  },
};

export default function AboutPage() {
  return <AboutPageClient />;
}
