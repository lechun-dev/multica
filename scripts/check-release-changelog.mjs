#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const CHANGELOG_LOCALE_FILES = [
  "apps/web/features/landing/i18n/en.ts",
  "apps/web/features/landing/i18n/ja.ts",
  "apps/web/features/landing/i18n/ko.ts",
  "apps/web/features/landing/i18n/zh.ts",
];

const RELEASE_TAG_PATTERN =
  /^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/;

export function parseReleaseTag(tag) {
  const match = RELEASE_TAG_PATTERN.exec(tag);
  if (!match) {
    throw new Error(
      `Release tag must look like vX.Y.Z or vX.Y.Z-suffix; got "${tag}".`,
    );
  }

  return {
    tag,
    version: `${match[1]}.${match[2]}.${match[3]}`,
    parts: match.slice(1, 4).map(Number),
    prerelease: match[4] ?? null,
  };
}

export function changelogVersionForTag(tag) {
  return parseReleaseTag(tag).version;
}

export function releaseRequiresChangelog(tag) {
  return parseReleaseTag(tag).prerelease === null;
}

function compareVersionParts(left, right) {
  for (let index = 0; index < 3; index += 1) {
    if (left[index] !== right[index]) {
      return left[index] - right[index];
    }
  }
  return 0;
}

export function validateReleaseTagSequence({ tag, existingTags }) {
  const candidate = parseReleaseTag(tag);
  if (!candidate.prerelease) {
    return { tag, latestStableTag: null };
  }

  const stableTags = existingTags
    .map((existingTag) => {
      try {
        return parseReleaseTag(existingTag);
      } catch {
        return null;
      }
    })
    .filter((existingTag) => existingTag && !existingTag.prerelease)
    .sort((left, right) => compareVersionParts(right.parts, left.parts));
  const latestStable = stableTags[0];

  // 2026-09-15 coder(lq): Once a base version is stable, all later preview
  // builds must move to a newer base so updater ordering remains monotonic.
  if (
    latestStable &&
    compareVersionParts(candidate.parts, latestStable.parts) <= 0
  ) {
    throw new Error(
      `Prerelease ${tag} must use a version newer than latest stable ${latestStable.tag}. Start the next preview line instead.`,
    );
  }

  return { tag, latestStableTag: latestStable?.tag ?? null };
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

    const existingTags = execFileSync("git", ["tag", "--list"], {
      cwd: repoRoot,
      encoding: "utf8",
    })
      .split("\n")
      .filter(Boolean);
    validateReleaseTagSequence({ tag, existingTags });
    if (releaseRequiresChangelog(tag)) {
      const result = validateReleaseChangelog({ tag, repoRoot });
      console.log(
        `Release changelog is ready: ${result.tag} -> ${result.version} (${CHANGELOG_LOCALE_FILES.length} locales).`,
      );
    } else {
      // 2026-09-22 coder(lq): Product notes are finalized for the stable tag;
      // prereleases still enforce tag ordering without requiring draft notes.
      console.log(`Prerelease tag is ready: ${tag}; changelog check skipped.`);
    }
  } catch (error) {
    console.error(`::error::${error.message}`);
    process.exitCode = 1;
  }
}
