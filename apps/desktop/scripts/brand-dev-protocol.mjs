export const DEV_PROTOCOL = "multica-dev";

export function ensureDevProtocol(urlTypes, bundleId) {
  const entries = Array.isArray(urlTypes) ? urlTypes : [];
  const alreadyRegistered = entries.some(
    (entry) =>
      entry &&
      typeof entry === "object" &&
      Array.isArray(entry.CFBundleURLSchemes) &&
      entry.CFBundleURLSchemes.includes(DEV_PROTOCOL),
  );
  if (alreadyRegistered) return entries;

  return [
    ...entries,
    {
      CFBundleTypeRole: "Editor",
      CFBundleURLName: bundleId,
      CFBundleURLSchemes: [DEV_PROTOCOL],
    },
  ];
}
