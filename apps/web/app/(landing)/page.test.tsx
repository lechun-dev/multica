import { describe, expect, it, vi } from "vitest";

const { mockRedirect } = vi.hoisted(() => ({
  mockRedirect: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  redirect: mockRedirect,
}));

import LandingPage from "./page";

describe("LandingPage", () => {
  it("redirects the root route to login", () => {
    LandingPage();

    expect(mockRedirect).toHaveBeenCalledWith("/login");
  });
});
