export const DWS_AUTH_REQUIRED_CHANNEL = "dws:auth-required";
export const DWS_AUTH_RESOLVED_CHANNEL = "dws:auth-resolved";
export const DWS_STATUS_NOTICE_CHANNEL = "dws:status-notice";

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

export type DwsStatusNoticeCode =
  | "auth_check_failed"
  | "identity_check_failed"
  | "recipient_identity_unresolved"
  | "send_failed"
  | "delivery_tracking_failed"
  | "delivery_failed";

export interface DwsStatusNotice {
  code: DwsStatusNoticeCode;
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
      return null;
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

export function dwsStatusNoticeFromPersonalMessage(
  state: string | undefined,
  message?: string,
): DwsStatusNotice | null {
  let code: DwsStatusNoticeCode;
  switch (state) {
    case "dws_auth_check_failed":
      code = "auth_check_failed";
      break;
    case "dws_identity_check_failed":
      code = "identity_check_failed";
      break;
    case "dws_recipient_identity_unresolved":
      code = "recipient_identity_unresolved";
      break;
    case "dws_send_failed":
      code = "send_failed";
      break;
    case "dws_missing_open_task_id":
    case "submit_checkpoint_failed":
    case "dws_status_query_failed":
      code = "delivery_tracking_failed";
      break;
    case "dws_delivery_failed":
      code = "delivery_failed";
      break;
    default:
      return null;
  }
  return {
    code,
    source: "dingtalk_personal_message",
    message,
  };
}

export function dwsStatusNoticeKey(notice: DwsStatusNotice | null): string {
  if (!notice) return "";
  return `${notice.source}:${notice.code}`;
}
