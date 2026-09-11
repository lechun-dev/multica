#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const CHANGELOG_LOCALE_FILES = [
  "apps/web/features/landing/i18n/en.ts",
  "apps/web/features/landing/i18n/ja.ts",
  "apps/web/features/landing/i18n/ko.ts",
  "apps/web/features/landing/i18n/zh.ts",
];

const RELEASE_TAG_PATTERN = /^v(\d+\.\d+\.\d+)(?:-[0-9A-Za-z.-]+)?$/;

export function changelogVersionForTag(tag) {
  const match = RELEASE_TAG_PATTERN.exec(tag);
  if (!match) {
    throw new Error(
      `Release tag must look like vX.Y.Z or vX.Y.Z-suffix; got "${tag}".`,
    );
  }

  return match[1];
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function validateReleaseChangelog({ tag, repoRoot }) {
  const version = changelogVersionForTag(tag);
  const versionEntryPattern = new RegExp(
    `\\bversion\\s*:\\s*["']${escapeRegExp(version)}["']`,
  );
  const missingFiles = CHANGELOG_LOCALE_FILES.filter((relativePath) => {
    const source = readFileSync(resolve(repoRoot, relativePath), "utf8");
    return !versionEntryPattern.test(source);
  });

  if (missingFiles.length > 0) {
    throw new Error(
      `Release ${tag} requires changelog version ${version} in every locale. Missing: ${missingFiles.join(
        ", ",
      )}`,
    );
  }

  return { tag, version };
}

const isCli =
  process.argv[1] &&
  fileURLToPath(import.meta.url) === resolve(process.argv[1]);

if (isCli) {
  const tag = process.argv[2] ?? process.env.GITHUB_REF_NAME;
  const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

  try {
    if (!tag) {
      throw new Error(
        "Provide a release tag as the first argument or GITHUB_REF_NAME.",
      );
    }

    // 2026-09-11 coder(lq): Prereleases share the base version's product
    // notes, so v0.4.82-beta.1 intentionally validates the 0.4.82 entry.
    const result = validateReleaseChangelog({ tag, repoRoot });
    console.log(
      `Release changelog is ready: ${result.tag} -> ${result.version} (${CHANGELOG_LOCALE_FILES.length} locales).`,
    );
  } catch (error) {
    console.error(`::error::${error.message}`);
    process.exitCode = 1;
  }
}
