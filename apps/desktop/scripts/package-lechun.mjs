#!/usr/bin/env node
// Dedicated entry point for the Lechun distributable. It selects the custom
// electron-builder config and injects the build-time Vite variant without
// changing the official package command or relying on shell-specific env
// assignment syntax.

import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const packageScript = resolve(here, "package.mjs");

const result = spawnSync(
  process.execPath,
  [
    packageScript,
    "--config",
    "electron-builder.lechun.yml",
    ...process.argv.slice(2),
  ],
  {
    stdio: "inherit",
    cwd: resolve(here, ".."),
    env: { ...process.env, VITE_MULTICA_DESKTOP_VARIANT: "lechun" },
  },
);

if (result.error) {
  console.error(
    `[package:lechun] failed to run package script: ${result.error.message}`,
  );
  process.exit(1);
}
process.exit(result.status ?? 1);
