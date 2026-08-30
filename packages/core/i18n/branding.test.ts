import { describe, expect, it } from "vitest";
import { brandLocaleResources, brandText, DEFAULT_PRODUCT_NAME } from "./branding";

describe("branding", () => {
  it("replaces display names while preserving code-like words", () => {
    expect(brandText("Welcome to Multica (Mission)", "MissionOS")).toBe(
      "Welcome to MissionOS (MissionOS)",
    );
    expect(brandText("MulticaIcon", "MissionOS")).toBe("MulticaIcon");
    expect(brandText("Multica", DEFAULT_PRODUCT_NAME)).toBe("MissionOS");
  });

  it("deep clones locale values without mutating the source", () => {
    const source = {
      en: {
        common: { title: "Multica", nested: ["Mission", "MulticaIcon"] },
      },
    };
    const branded = brandLocaleResources(source, "MissionOS");

    expect(branded.en?.common).toEqual({
      title: "MissionOS",
      nested: ["MissionOS", "MulticaIcon"],
    });
    expect(source.en.common).toEqual({
      title: "Multica",
      nested: ["Mission", "MulticaIcon"],
    });
    expect(branded).not.toBe(source);
  });
});
