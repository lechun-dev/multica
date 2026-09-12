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

const RELEASE_TAG_PATTERN = /^v(\d+\.\d+\.\d+)(?:-([0-9A-Za-z.-]+))?$/;

export function isPrereleaseTag(tag) {
  const match = RELEASE_TAG_PATTERN.exec(tag);
  if (!match) {
    throw new Error(
      `Release tag must look like vX.Y.Z or vX.Y.Z-suffix; got "${tag}".`,
    );
  }

  return Boolean(match[2]);
}

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

  // 2026-09-12 coder(lq): Prerelease builds must not require or publish the
  // final version's notes before that stable version is released.
  if (isPrereleaseTag(tag)) {
    return { tag, version, skipped: true };
  }

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

    const result = validateReleaseChangelog({ tag, repoRoot });
    if (result.skipped) {
      console.log(
        `Prerelease ${result.tag} does not require a stable changelog entry.`,
      );
    } else {
      console.log(
        `Release changelog is ready: ${result.tag} -> ${result.version} (${CHANGELOG_LOCALE_FILES.length} locales).`,
      );
    }
  } catch (error) {
    console.error(`::error::${error.message}`);
    process.exitCode = 1;
  }
}
