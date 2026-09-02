#!/usr/bin/env node
// Dedicated entry point for the Lechun distributable. It selects the custom
// electron-builder config and injects the build-time Vite variant without
// changing the official package command or relying on shell-specific env
// assignment syntax.

import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const packageScript = resolve(here, "package.mjs");
// pnpm inserts a standalone `--` before arguments forwarded to a package
// script. Remove it before injecting our own electron-builder config; if it
// remains between `--config` and the target flags, electron-builder stops
// parsing options and silently ignores `--publish never`, then attempts an
// implicit GitHub publish on tagged CI builds.
export function stripForwardedSeparator(argv) {
  return argv[0] === "--" ? argv.slice(1) : argv;
}

function main() {
  const forwardedArgs = stripForwardedSeparator(process.argv.slice(2));

  const result = spawnSync(
    process.execPath,
    [
      packageScript,
      "--config",
      "electron-builder.lechun.yml",
      ...forwardedArgs,
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
    return 1;
  }
  return result.status ?? 1;
}

// Only run when invoked as a CLI, not when imported by a test file.
if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  process.exit(main());
}
