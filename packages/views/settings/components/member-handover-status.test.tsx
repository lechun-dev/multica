import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { MemberWithUser } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { MemberHandoverStatus } from "./member-handover-status";

const completed: NonNullable<MemberWithUser["handover"]> = {
  status: "completed", reason: "", recipient_name: "Leader", attempted_at: null, notification_status: "staged",
};

describe("member handover diagnostics", () => {
  it("shows why a pending handover needs manual intervention", () => {
    renderWithI18n(<MemberHandoverStatus handover={{ ...completed, status: "pending", recipient_name: "", reason: "manager_unlinked", notification_status: "" }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Needs attention");
    expect(screen.getByRole("status")).toHaveTextContent("Manager has no workspace account");
  });

  it("keeps completed work separate from queued notification delivery", () => {
    renderWithI18n(<MemberHandoverStatus handover={completed} />);
    expect(screen.getByRole("status")).toHaveTextContent("Permissions opened · Leader · Queued for Mission delivery");
    expect(screen.getByRole("status")).not.toHaveTextContent("Mission delivered");
  });

  it.each([
    ["delivered", "Mission delivered"],
    ["failed", "Mission delivery failed; contact support to investigate"],
    ["pending", "Mission notification awaiting routing"],
  ] as const)("shows notification phase %s without changing the handover outcome", (notificationStatus, label) => {
    renderWithI18n(<MemberHandoverStatus handover={{ ...completed, notification_status: notificationStatus }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Permissions opened");
    expect(screen.getByRole("status")).toHaveTextContent(label);
    expect(screen.getByRole("status")).not.toHaveTextContent("Permission opening failed");
  });

  it("gives a recovery path for execution failures", () => {
    renderWithI18n(<MemberHandoverStatus handover={{ ...completed, status: "failed", reason: "execution_failed", notification_status: "" }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Permission opening failed");
    expect(screen.getByRole("status")).toHaveTextContent("Permission opening failed; background processing will retry");
  });
});
