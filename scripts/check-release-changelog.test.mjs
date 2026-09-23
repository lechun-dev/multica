import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import test from "node:test";

import {
  CHANGELOG_LOCALE_FILES,
  changelogVersionForTag,
  releaseRequiresChangelog,
  validateReleaseTagSequence,
  validateReleaseChangelog,
} from "./check-release-changelog.mjs";

function createFixture(versionsByFile = {}) {
  const repoRoot = mkdtempSync(resolve(tmpdir(), "release-changelog-"));

  for (const relativePath of CHANGELOG_LOCALE_FILES) {
    const absolutePath = resolve(repoRoot, relativePath);
    mkdirSync(dirname(absolutePath), { recursive: true });
    const version = versionsByFile[relativePath] ?? "0.4.82";
    writeFileSync(
      absolutePath,
      `export const messages = { entries: [{ version: "${version}" }] };\n`,
    );
  }

  return repoRoot;
}

test("accepts a stable release when every locale has the version", (t) => {
  const repoRoot = createFixture();
  t.after(() => rmSync(repoRoot, { recursive: true, force: true }));

  assert.deepEqual(validateReleaseChangelog({ tag: "v0.4.82", repoRoot }), {
    tag: "v0.4.82",
    version: "0.4.82",
  });
});

test("requires product changelogs only for stable releases", () => {
  assert.equal(changelogVersionForTag("v0.4.82-beta.2"), "0.4.82");
  assert.equal(releaseRequiresChangelog("v0.4.82"), true);
  assert.equal(releaseRequiresChangelog("v0.4.82-beta.2"), false);
  assert.equal(releaseRequiresChangelog("v0.4.82-test.1"), false);
});

test("reports every locale missing the release version", (t) => {
  const missingFile = "apps/web/features/landing/i18n/ko.ts";
  const repoRoot = createFixture({ [missingFile]: "0.4.81" });
  t.after(() => rmSync(repoRoot, { recursive: true, force: true }));

  assert.throws(
    () => validateReleaseChangelog({ tag: "v0.4.82", repoRoot }),
    new RegExp(missingFile.replaceAll(".", "\\.")),
  );
});

test("requires an exact version instead of a longer prefix", (t) => {
  const versions = Object.fromEntries(
    CHANGELOG_LOCALE_FILES.map((relativePath) => [relativePath, "0.4.820"]),
  );
  const repoRoot = createFixture(versions);
  t.after(() => rmSync(repoRoot, { recursive: true, force: true }));

  assert.throws(() =>
    validateReleaseChangelog({ tag: "v0.4.82", repoRoot }),
  );
});

test("rejects malformed release tags", () => {
  assert.throws(
    () => changelogVersionForTag("release-0.4.82"),
    /Release tag must look like/,
  );
});

test("accepts a prerelease newer than the latest stable version", () => {
  assert.deepEqual(
    validateReleaseTagSequence({
      tag: "v0.4.86-beta.1",
      existingTags: ["v0.4.85", "v0.4.85-beta.5"],
    }),
    { tag: "v0.4.86-beta.1", latestStableTag: "v0.4.85" },
  );
});

test("rejects a prerelease on an already stable version", () => {
  assert.throws(
    () =>
      validateReleaseTagSequence({
        tag: "v0.4.85-beta.6",
        existingTags: ["v0.4.84", "v0.4.85"],
      }),
    /must use a version newer than latest stable v0\.4\.85/,
  );
});

test("allows rebuilding an existing prerelease on a stable version", () => {
  assert.deepEqual(
    validateReleaseTagSequence({
      tag: "v0.4.85-beta.5",
      existingTags: ["v0.4.84", "v0.4.85-beta.5", "v0.4.85"],
      allowExistingPrerelease: true,
    }),
    { tag: "v0.4.85-beta.5", latestStableTag: "v0.4.85" },
  );
});

test("does not allow a new prerelease on a stable version during rebuild", () => {
  assert.throws(
    () =>
      validateReleaseTagSequence({
        tag: "v0.4.85-beta.6",
        existingTags: ["v0.4.84", "v0.4.85-beta.5", "v0.4.85"],
        allowExistingPrerelease: true,
      }),
    /must use a version newer than latest stable v0\.4\.85/,
  );
});

test("ignores prereleases when finding the latest stable version", () => {
  assert.deepEqual(
    validateReleaseTagSequence({
      tag: "v0.4.86-beta.2",
      existingTags: ["v0.4.85", "v0.4.99-beta.1"],
    }),
    { tag: "v0.4.86-beta.2", latestStableTag: "v0.4.85" },
  );
});
