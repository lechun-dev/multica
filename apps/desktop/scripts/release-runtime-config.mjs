const DEFAULT_STAGING_RUNTIME_CONFIG = Object.freeze({
  apiUrl: "https://mission-staging.lechun.cc",
  wsUrl: "wss://mission-staging.lechun.cc/ws",
  appUrl: "https://mission-staging.lechun.cc",
});

const BLOCKED_OFFICIAL_HOSTS = new Set([
  "api.multica.ai",
  "multica.ai",
  "multica-api.copilothub.ai",
  "multica-app.copilothub.ai",
]);

function validateUrl(value, field, protocols) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`[release-runtime-config] ${field} is not a valid URL`);
  }

  if (!protocols.includes(parsed.protocol)) {
    throw new Error(
      `[release-runtime-config] ${field} must use ${protocols.join(" or ")}`,
    );
  }
  if (BLOCKED_OFFICIAL_HOSTS.has(parsed.hostname.toLowerCase())) {
    throw new Error(
      `[release-runtime-config] ${field} must not point a Lechun prerelease at ${parsed.hostname}`,
    );
  }
  return value.replace(/\/+$/, "");
}

export function isPrereleaseTag(tag) {
  return /^v\d+\.\d+\.\d+-[0-9A-Za-z.-]+$/.test(tag ?? "");
}

export function resolveReleaseRuntimeConfig(tag, env = process.env) {
  if (!isPrereleaseTag(tag)) return null;

  return {
    apiUrl: validateUrl(
      env.STAGING_API_URL?.trim() || DEFAULT_STAGING_RUNTIME_CONFIG.apiUrl,
      "STAGING_API_URL",
      ["https:"],
    ),
    wsUrl: validateUrl(
      env.STAGING_WS_URL?.trim() || DEFAULT_STAGING_RUNTIME_CONFIG.wsUrl,
      "STAGING_WS_URL",
      ["wss:"],
    ),
    appUrl: validateUrl(
      env.STAGING_APP_URL?.trim() || DEFAULT_STAGING_RUNTIME_CONFIG.appUrl,
      "STAGING_APP_URL",
      ["https:"],
    ),
  };
}

/**
 * Inject prerelease endpoints before electron-vite compiles import.meta.env.
 * Stable builds intentionally leave VITE_* unset and use the private
 * production defaults from shared/runtime-config.ts.
 */
export function applyReleaseRuntimeConfig(
  env = process.env,
  tag = env.RELEASE_TAG || env.GITHUB_REF_NAME,
) {
  const config = resolveReleaseRuntimeConfig(tag, env);
  if (!config) return null;

  env.VITE_API_URL = config.apiUrl;
  env.VITE_WS_URL = config.wsUrl;
  env.VITE_APP_URL = config.appUrl;
  return config;
}
