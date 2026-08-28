export type DingTalkOAuthClient =
  | "web"
  | "desktop"
  | "desktop-dev"
  | "desktop-lechun";

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
  if (client !== "web") {
    url.searchParams.set("client", client);
  }
  return url.toString();
}

export function isDesktopDingTalkState(state: string): boolean {
  return (
    state.endsWith(".desktop") ||
    state.endsWith(".desktop-dev") ||
    state.endsWith(".desktop-lechun")
  );
}

/** Return the deep-link protocol for a verified desktop DingTalk state. */
export function dingtalkCallbackProtocol(
  state: string,
): "multica" | "multica-dev" | "multica-lechun" {
  if (state.endsWith(".desktop-dev")) return "multica-dev";
  if (state.endsWith(".desktop-lechun")) return "multica-lechun";
  return "multica";
}
