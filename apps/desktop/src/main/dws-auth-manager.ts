import { execFile } from "child_process";
import { constants as fsConstants } from "fs";
import { access } from "fs/promises";
import { homedir } from "os";
import { delimiter, join } from "path";
import type { DwsAuthStatus, DwsLoginResult } from "../shared/dws-auth";

const DWS_STATUS_TIMEOUT_MS = 35_000;
const DWS_LOGIN_TIMEOUT_MS = 10 * 60_000;
const DWS_OUTPUT_LIMIT_BYTES = 1024 * 1024;

let loginInFlight: Promise<DwsLoginResult> | null = null;

const DWS_INVALID_CLIENT_CREDENTIALS_MESSAGE =
  "DWS OAuth application credentials are invalid or no longer available.";

function stripANSI(value: string): string {
  // 2026-09-11 coder(lq): Build ESC outside the regex literal so ESLint's
  // no-control-regex guard stays effective without changing ANSI stripping.
  const ansiSequence = new RegExp(
    `${String.fromCharCode(27)}\\[[0-?]*[ -/]*[@-~]`,
    "g",
  );
  return value.replace(ansiSequence, "");
}

export function parseTrailingDwsJSON(
  output: string,
): Record<string, unknown> | null {
  const text = stripANSI(output).trim();
  let start = text.indexOf("{");
  while (start >= 0) {
    try {
      const parsed: unknown = JSON.parse(text.slice(start));
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // DWS may print OAuth progress before its final JSON object.
    }
    start = text.indexOf("{", start + 1);
  }
  return null;
}

export function dwsErrorIsUnauthenticated(message: string): boolean {
  const normalized = message.toLowerCase();
  return (
    normalized.includes("未登录") ||
    normalized.includes("not logged") ||
    normalized.includes("auth_token_expired") ||
    normalized.includes("user_token_illegal") ||
    normalized.includes("token验证失败")
  );
}

export function dwsErrorIsInvalidClientCredentials(message: string): boolean {
  const normalized = message.toLowerCase();
  return (
    normalized.includes("invalidparameter.idorsecret.notfound") ||
    normalized.includes("clientid或者clientsecret错误") ||
    normalized.includes("clientid or clientsecret") ||
    normalized.includes(DWS_INVALID_CLIENT_CREDENTIALS_MESSAGE.toLowerCase())
  );
}

function readString(
  value: Record<string, unknown>,
  ...keys: string[]
): string | undefined {
  for (const key of keys) {
    const candidate = value[key];
    if (typeof candidate === "string" && candidate.trim()) {
      return candidate.trim();
    }
  }
  return undefined;
}

function compactDwsError(output: string): string {
  const text = stripANSI(output).trim();
  if (!text) return "DWS command failed";
  if (dwsErrorIsInvalidClientCredentials(text)) {
    return DWS_INVALID_CLIENT_CREDENTIALS_MESSAGE;
  }
  const lower = text.toLowerCase();
  if (
    lower.includes("access_token") ||
    lower.includes("refresh_token") ||
    lower.includes("client_secret")
  ) {
    return "DWS returned a credential-related error; details were hidden.";
  }
  return text.length > 500 ? `${text.slice(0, 500)}...` : text;
}

async function isExecutable(path: string): Promise<boolean> {
  try {
    await access(
      path,
      process.platform === "win32" ? fsConstants.F_OK : fsConstants.X_OK,
    );
    return true;
  } catch {
    return false;
  }
}

export async function resolveDwsExecutable(): Promise<string | null> {
  const configured = process.env.MULTICA_DWS_PATH?.trim();
  if (configured) return (await isExecutable(configured)) ? configured : null;

  const pathEntries = (process.env.PATH ?? "")
    .split(delimiter)
    .filter(Boolean);
  if (process.platform === "darwin") {
    pathEntries.push(
      "/opt/homebrew/bin",
      "/usr/local/bin",
      join(homedir(), ".local", "bin"),
    );
  }
  const extensions =
    process.platform === "win32"
      ? (process.env.PATHEXT ?? ".EXE;.CMD;.BAT")
          .split(";")
          .filter(Boolean)
      : [""];
  for (const directory of pathEntries) {
    for (const extension of extensions) {
      const candidate = join(
        directory,
        process.platform === "win32" ? `dws${extension.toLowerCase()}` : "dws",
      );
      if (await isExecutable(candidate)) return candidate;
    }
  }
  return null;
}

function runDws(
  executable: string,
  args: string[],
  timeout: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(
      executable,
      args,
      {
        encoding: "utf8",
        env: process.env,
        maxBuffer: DWS_OUTPUT_LIMIT_BYTES,
        timeout,
        windowsHide: true,
      },
      (error, stdout, stderr) => {
        // DWS reserves stdout for its final JSON and may write OAuth progress
        // to stderr. Keep stdout last so the trailing-object parser sees the
        // machine-readable result even when both streams contain content.
        const output = `${stderr ?? ""}\n${stdout ?? ""}`.trim();
        if (error) {
          reject(new Error(compactDwsError(output || error.message)));
          return;
        }
        resolve(output);
      },
    );
  });
}

export async function getDwsAuthStatus(): Promise<DwsAuthStatus> {
  const executable = await resolveDwsExecutable();
  if (!executable) return { state: "not_installed" };
  try {
    const output = await runDws(
      executable,
      ["auth", "status", "--format", "json"],
      DWS_STATUS_TIMEOUT_MS,
    );
    const payload = parseTrailingDwsJSON(output);
    if (!payload) {
      return { state: "error", message: "DWS returned an invalid response." };
    }
    if (payload.authenticated !== true) return { state: "not_logged_in" };
    return {
      state: "authenticated",
      userName: readString(payload, "user_name", "userName"),
      corpName: readString(payload, "corp_name", "corpName"),
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (dwsErrorIsUnauthenticated(message)) {
      return { state: "not_logged_in" };
    }
    return {
      state: "error",
      message,
    };
  }
}

async function performDwsLogin(): Promise<DwsLoginResult> {
  const executable = await resolveDwsExecutable();
  if (!executable) {
    return {
      ok: false,
      reason: "not_installed",
      message: "DWS CLI is not installed or is not available on PATH.",
    };
  }
  try {
    const output = await runDws(
      executable,
      ["auth", "login", "--format", "json"],
      DWS_LOGIN_TIMEOUT_MS,
    );
    const payload = parseTrailingDwsJSON(output);
    if (!payload || payload.success !== true) {
      return {
        ok: false,
        reason: "failed",
        message:
          (payload && readString(payload, "message")) ||
          "DWS authorization did not complete.",
      };
    }
    const status = await getDwsAuthStatus();
    if (status.state !== "authenticated") {
      return {
        ok: false,
        reason:
          status.state === "not_installed"
            ? "not_installed"
            : status.state === "not_logged_in"
              ? "not_logged_in"
              : "failed",
        message:
          status.state === "error"
            ? status.message
            : "DWS authorization completed, but no authenticated profile is active.",
      };
    }
    return {
      ok: true,
      userName: status.userName,
      corpName: status.corpName,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return {
      ok: false,
      reason: dwsErrorIsInvalidClientCredentials(message)
        ? "invalid_client_credentials"
        : "failed",
      message,
    };
  }
}

export function loginDws(): Promise<DwsLoginResult> {
  if (loginInFlight) return loginInFlight;
  loginInFlight = performDwsLogin().finally(() => {
    loginInFlight = null;
  });
  return loginInFlight;
}
