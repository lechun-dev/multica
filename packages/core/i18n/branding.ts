import type { LocaleResources } from "./types";

/** Default product name used by the MissionOS build. */
export const DEFAULT_PRODUCT_NAME = "MissionOS";

const UPSTREAM_BRAND_PATTERN = /\b(?:Multica|Mission)\b/g;

/** Replace upstream product references in copied locale data without mutation. */
export function brandText(value: string, productName: string): string {
  return value.replace(UPSTREAM_BRAND_PATTERN, productName);
}

function brandValue(value: unknown, productName: string): unknown {
  if (typeof value === "string") return brandText(value, productName);
  if (Array.isArray(value)) return value.map((item) => brandValue(item, productName));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, brandValue(item, productName)]),
    );
  }
  return value;
}

/** Apply the same display-name transform to any typed, nested content object. */
export function brandObject<T>(value: T, productName: string): T {
  return brandValue(value, productName) as T;
}

/**
 * Build-time branding transform for locale resources.
 *
 * 2026-08-30 coder(lq): Keep branding additive so upstream locale files can be
 * upgraded independently and the imported resource objects remain immutable.
 */
export function brandLocaleResources(
  resources: Record<string, LocaleResources>,
  productName = DEFAULT_PRODUCT_NAME,
): Record<string, LocaleResources> {
  return brandObject(resources, productName);
}
