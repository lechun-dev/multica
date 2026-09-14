const storageKey = "multica.dingtalk-dws.authorization-started-at";

export const dingTalkDWSAuthorizationTimeoutMs = 2 * 60 * 1000;

function sessionStorageOrNull(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

export function readDingTalkDWSAuthorizationAttempt(): number | null {
  const raw = sessionStorageOrNull()?.getItem(storageKey);
  if (!raw) return null;
  const startedAt = Number(raw);
  return Number.isFinite(startedAt) && startedAt > 0 ? startedAt : null;
}

export function beginDingTalkDWSAuthorizationAttempt(
  startedAt = Date.now(),
): number {
  sessionStorageOrNull()?.setItem(storageKey, String(startedAt));
  return startedAt;
}

export function clearDingTalkDWSAuthorizationAttempt(): void {
  sessionStorageOrNull()?.removeItem(storageKey);
}
