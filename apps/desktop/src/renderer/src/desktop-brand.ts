import { resolveDesktopIdentity } from "../../shared/desktop-identity";

/** 2026-08-29 lq: Keep display copy separate from stable update identifiers. */
export const DESKTOP_PRODUCT_NAME = resolveDesktopIdentity({
  isDev: import.meta.env.DEV,
  variant: import.meta.env.VITE_MULTICA_DESKTOP_VARIANT,
}).productName;
