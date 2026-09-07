import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const translate = vi.hoisted(() => vi.fn());

vi.mock("./use-t", () => ({
  useT: () => ({ t: translate }),
}));

import { useTimeAgo } from "./use-time-ago";

describe("useTimeAgo", () => {
  beforeEach(() => {
    translate.mockClear();
  });

  it.each(["", "not-a-date"])(
    "returns an empty label for an invalid timestamp (%j)",
    (timestamp) => {
      const { result } = renderHook(() => useTimeAgo());

      expect(result.current(timestamp)).toBe("");
      expect(translate).not.toHaveBeenCalled();
    },
  );
});
