import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import test from "node:test";

import {
  CHANGELOG_LOCALE_FILES,
  changelogVersionForTag,
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

test("maps a prerelease tag to its base changelog version", (t) => {
  const repoRoot = createFixture();
  t.after(() => rmSync(repoRoot, { recursive: true, force: true }));

  assert.equal(changelogVersionForTag("v0.4.82-beta.2"), "0.4.82");
  assert.doesNotThrow(() =>
    validateReleaseChangelog({ tag: "v0.4.82-beta.2", repoRoot }),
  );
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
