import { redirect } from "next/navigation";

export default function LandingPage() {
  // 2026-08-28 coder(lq): Self-hosted web entry should open the login page directly.
  redirect("/login");
}
