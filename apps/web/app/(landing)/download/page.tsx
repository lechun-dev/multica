import type { Metadata } from "next";
import { fetchLatestRelease } from "@/features/landing/utils/github-release";
import { DownloadClient } from "./download-client";
import { PRODUCT_NAME } from "@/config/product-brand";

// Vercel ISR: the server fetch inside fetchLatestRelease carries
// `next: { revalidate: 300 }`, which makes GitHub API cost at most
// one request per region per 5 minutes. Page-level revalidate mirrors
// that window so the first paint also refreshes every 5 minutes.
export const revalidate = 300;

export const metadata: Metadata = {
  title: `Download ${PRODUCT_NAME}`,
  description:
    `Download ${PRODUCT_NAME} for macOS, Windows, or Linux — or install the CLI for servers and remote dev boxes.`,
  openGraph: {
    title: `Download ${PRODUCT_NAME}`,
    description:
      `Get the ${PRODUCT_NAME} desktop app with a bundled daemon, or install the CLI for servers and remote dev boxes.`,
    url: "/download",
  },
  alternates: {
    canonical: "/download",
  },
};

export default async function DownloadPage() {
  const release = await fetchLatestRelease();
  return <DownloadClient release={release} />;
}
