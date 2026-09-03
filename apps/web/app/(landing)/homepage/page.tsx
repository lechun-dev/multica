import type { Metadata } from "next";
import { MulticaAppEntry } from "@/features/landing/components/multica-app-entry";
import { PRODUCT_NAME } from "@/config/product-brand";

export const metadata: Metadata = {
  title: "Homepage",
  description:
    `${PRODUCT_NAME} — open-source platform that turns coding agents into real teammates. Assign tasks, track progress, compound skills.`,
  openGraph: {
    title: `${PRODUCT_NAME} — Project Management for Human + Agent Teams`,
    description:
      "Manage your human + agent workforce in one place.",
    url: "/homepage",
  },
  alternates: {
    canonical: "/homepage",
  },
};

export default function HomepagePage() {
  return <MulticaAppEntry />;
}
