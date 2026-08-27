import type { ReactNode } from "react";
import styles from "./dingtalk-first-login-frame.module.css";

/**
 * Local presentation wrapper for the self-hosted login page.
 *
 * LoginPage owns all authentication state and handlers. This wrapper only
 * provides the shared DingTalk-first layout surface for Web and Desktop.
 */
export function DingTalkFirstLoginFrame({ children }: { children: ReactNode }) {
  return <div className={styles.frame}>{children}</div>;
}
