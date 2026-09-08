export const DWS_AUTH_REQUIRED_CHANNEL = "dws:auth-required";
export const DWS_AUTH_RESOLVED_CHANNEL = "dws:auth-resolved";

export type DwsAuthReason =
  | "not_installed"
  | "not_logged_in"
  | "identity_mismatch";

export interface DwsAuthRequirement {
  reason: DwsAuthReason;
  source: string;
  message?: string;
}

export interface DwsAuthRequest {
  source: string;
  message?: string;
}

export type DwsAuthStatus =
  | {
      state: "authenticated";
      userName?: string;
      corpName?: string;
    }
  | { state: "not_logged_in" }
  | { state: "not_installed" }
  | { state: "error"; message: string };

export type DwsLoginResult =
  | {
      ok: true;
      userName?: string;
      corpName?: string;
    }
  | {
      ok: false;
      reason: "not_installed" | "not_logged_in" | "failed";
      message: string;
    };

export function dwsRequirementFromAuthStatus(
  status: DwsAuthStatus,
  request: DwsAuthRequest,
): DwsAuthRequirement | null {
  switch (status.state) {
    case "authenticated":
      return null;
    case "not_installed":
      return {
        reason: "not_installed",
        source: request.source,
        message: request.message,
      };
    case "not_logged_in":
      return {
        reason: "not_logged_in",
        source: request.source,
        message: request.message,
      };
    case "error":
      return {
        reason: "not_logged_in",
        source: request.source,
        message: request.message || status.message,
      };
  }
}

export function dwsRequirementFromPersonalMessage(
  state: string | undefined,
  message?: string,
): DwsAuthRequirement | null {
  switch (state) {
    case "dws_not_installed":
      return {
        reason: "not_installed",
        source: "dingtalk_personal_message",
        message,
      };
    case "dws_not_logged_in":
      return {
        reason: "not_logged_in",
        source: "dingtalk_personal_message",
        message,
      };
    case "dws_identity_mismatch":
      return {
        reason: "identity_mismatch",
        source: "dingtalk_personal_message",
        message,
      };
    default:
      return null;
  }
}

export function dwsRequirementKey(
  requirement: DwsAuthRequirement | null,
): string {
  if (!requirement) return "";
  return `${requirement.source}:${requirement.reason}:${requirement.message ?? ""}`;
}
