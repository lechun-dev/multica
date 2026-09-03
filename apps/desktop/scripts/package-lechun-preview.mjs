#!/usr/bin/env node
// 2026-09-03 coder(lq): Build the isolated Lechun preview distribution. Keep
// this wrapper separate from the stable command so release workflows can make
// the variant explicit and future upstream packaging changes remain local.

import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const packageScript = resolve(here, "package.mjs");
const iconScript = resolve(here, "generate-beta-icons.mjs");

export function stripForwardedSeparator(argv) {
  return argv[0] === "--" ? argv.slice(1) : argv;
}

function main() {
  const forwardedArgs = stripForwardedSeparator(process.argv.slice(2));
  // 2026-09-03 coder(lq): Generate the Beta-marked resources at build time so
  // local preview packages and clean GitHub runners use the same icon set.
  const iconResult = spawnSync(process.execPath, [iconScript], {
    stdio: "inherit",
    cwd: resolve(here, ".."),
  });
  if (iconResult.error) {
    console.error(
      `[package:lechun-preview] failed to generate Beta icons: ${iconResult.error.message}`,
    );
    return 1;
  }
  if (iconResult.status !== 0) return iconResult.status ?? 1;

  const result = spawnSync(
    process.execPath,
    [
      packageScript,
      "--config",
      "electron-builder.lechun-preview.yml",
      ...forwardedArgs,
    ],
    {
      stdio: "inherit",
      cwd: resolve(here, ".."),
      env: { ...process.env, VITE_MULTICA_DESKTOP_VARIANT: "lechun-preview" },
    },
  );
  if (result.error) {
    console.error(
      `[package:lechun-preview] failed to run package script: ${result.error.message}`,
    );
    return 1;
  }
  return result.status ?? 1;
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  process.exit(main());
}
