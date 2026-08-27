export type DingTalkOAuthClient = "web" | "desktop";

export function buildDingTalkLoginURL(
  apiBaseURL: string,
  client: DingTalkOAuthClient,
  fallbackOrigin?: string,
): string {
  const configuredBase = apiBaseURL.trim();
  const base = /^https?:\/\//i.test(configuredBase)
    ? configuredBase
    : fallbackOrigin;
  if (!base) {
    throw new Error("DingTalk login requires an HTTP API origin");
  }
  const url = new URL("/auth/dingtalk/start", base);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("DingTalk login requires an HTTP API origin");
  }
  if (client === "desktop") {
    url.searchParams.set("client", "desktop");
  }
  return url.toString();
}

export function isDesktopDingTalkState(state: string): boolean {
  return state.endsWith(".desktop");
}
