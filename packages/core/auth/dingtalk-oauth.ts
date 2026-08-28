export type DingTalkOAuthClient =
  | "web"
  | "desktop"
  | "desktop-dev"
  | "desktop-lechun";

export function buildDingTalkLoginURL(
  apiBaseURL: string,
  client: DingTalkOAuthClient,
  fallbackOrigin?: string,
  next?: string | null,
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
  if (next) {
    url.searchParams.set("next", next);
  }
  return url.toString();
}

export function isDesktopDingTalkState(state: string): boolean {
  return dingtalkStateClient(state) !== "web";
}

function dingtalkStateClient(
  state: string,
): DingTalkOAuthClient {
  const withoutNext = state.split(".next.", 1)[0];
  if (withoutNext.endsWith(".desktop-dev")) return "desktop-dev";
  if (withoutNext.endsWith(".desktop-lechun")) return "desktop-lechun";
  if (withoutNext.endsWith(".desktop")) return "desktop";
  return "web";
}

/** Return the deep-link protocol for a verified desktop DingTalk state. */
export function dingtalkCallbackProtocol(
  state: string,
): "multica" | "multica-dev" | "multica-lechun" {
  const client = dingtalkStateClient(state);
  if (client === "desktop-dev") return "multica-dev";
  if (client === "desktop-lechun") return "multica-lechun";
  return "multica";
}

/** Decode the optional same-origin path carried by a verified OAuth state. */
export function dingtalkNextFromState(state: string): string | null {
  const marker = ".next.";
  const index = state.lastIndexOf(marker);
  if (index < 0) return null;
  const encoded = state.slice(index + marker.length);
  if (!encoded) return null;
  try {
    const normalized = encoded.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized + "=".repeat((4 - (normalized.length % 4)) % 4);
    const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return null;
  }
}
