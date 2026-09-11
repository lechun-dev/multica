// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  dwsRequirementFromAuthStatus,
  dwsRequirementFromPersonalMessage,
  dwsRequirementKey,
  dwsStatusNoticeFromPersonalMessage,
  dwsStatusNoticeKey,
} from "./dws-auth";

describe("DWS auth requirement mapping", () => {
  it("lets a reusable DWS action trigger the shared dialog only when needed", () => {
    const request = { source: "calendar_create" };
    expect(
      dwsRequirementFromAuthStatus({ state: "authenticated" }, request),
    ).toBeNull();
    expect(
      dwsRequirementFromAuthStatus({ state: "not_logged_in" }, request),
    ).toEqual({
      reason: "not_logged_in",
      source: "calendar_create",
      message: undefined,
    });
    expect(
      dwsRequirementFromAuthStatus({ state: "not_installed" }, request),
    ).toEqual({
      reason: "not_installed",
      source: "calendar_create",
      message: undefined,
    });
    expect(
      dwsRequirementFromAuthStatus(
        { state: "error", message: "DWS status check timed out" },
        request,
      ),
    ).toBeNull();
  });

  it.each([
    ["dws_not_installed", "not_installed"],
    ["dws_not_logged_in", "not_logged_in"],
    ["dws_identity_mismatch", "identity_mismatch"],
  ] as const)("maps %s to the shared %s dialog reason", (state, reason) => {
    expect(dwsRequirementFromPersonalMessage(state, "details")).toEqual({
      reason,
      source: "dingtalk_personal_message",
      message: "details",
    });
  });

  it("does not turn transient delivery failures into login prompts", () => {
    expect(
      dwsRequirementFromPersonalMessage("dws_send_failed", "retrying"),
    ).toBeNull();
    expect(dwsRequirementFromPersonalMessage("ready")).toBeNull();
  });

  it("de-duplicates transient notices by category instead of raw details", () => {
    expect(
      dwsStatusNoticeKey({
        code: "send_failed",
        source: "dingtalk_personal_message",
        message: "first attempt",
      }),
    ).toBe("dingtalk_personal_message:send_failed");
  });

  it("builds a stable de-duplication key", () => {
    const requirement = dwsRequirementFromPersonalMessage(
      "dws_not_logged_in",
      "sign in",
    );
    expect(dwsRequirementKey(requirement)).toBe(
      "dingtalk_personal_message:not_logged_in:sign in",
    );
  });
});

describe("DWS status notice mapping", () => {
  it.each([
    ["dws_auth_check_failed", "auth_check_failed"],
    ["dws_identity_check_failed", "identity_check_failed"],
    ["dws_recipient_identity_unresolved", "recipient_identity_unresolved"],
    ["dws_send_failed", "send_failed"],
    ["dws_missing_open_task_id", "delivery_tracking_failed"],
    ["submit_checkpoint_failed", "delivery_tracking_failed"],
    ["dws_status_query_failed", "delivery_tracking_failed"],
    ["dws_delivery_failed", "delivery_failed"],
  ] as const)("maps %s to %s", (state, code) => {
    expect(dwsStatusNoticeFromPersonalMessage(state, "details")).toEqual({
      code,
      source: "dingtalk_personal_message",
      message: "details",
    });
  });

  it.each([
    "dws_not_installed",
    "dws_not_logged_in",
    "dws_identity_mismatch",
    "dws_delivery_pending",
    "ready",
  ])("does not turn %s into a transient error notice", (state) => {
    expect(dwsStatusNoticeFromPersonalMessage(state)).toBeNull();
  });
});
