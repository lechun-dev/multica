import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync, statSync, readdirSync, symlinkSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import {
  classifyDeployTag,
  validateDeployTag,
} from "./validate-deploy-tag.mjs";

function readWorkflow(name) {
  return readFileSync(new URL(`../.github/workflows/${name}`, import.meta.url), "utf8");
}

test("classifies supported immutable release tags", () => {
  assert.equal(classifyDeployTag("v0.4.86"), "stable");
  assert.equal(classifyDeployTag("v0.4.86-beta.1"), "beta");
  assert.equal(classifyDeployTag("v0.4.86-test.12"), "test");
});

test("rejects mutable, malformed, and unsupported prerelease tags", () => {
  for (const tag of [
    "",
    "latest",
    "sha-deadbeef",
    "0.4.86",
    "v01.4.86",
    "v0.4.86-beta.0",
    "v0.4.86-rc.1",
  ]) {
    assert.equal(classifyDeployTag(tag), null, tag);
  }
});

test("production accepts only stable tags", () => {
  assert.equal(validateDeployTag("production", "v0.4.86"), "stable");
  assert.throws(
    () => validateDeployTag("production", "v0.4.86-beta.1"),
    /production cannot deploy/,
  );
  assert.throws(
    () => validateDeployTag("production", "v0.4.86-test.1"),
    /production cannot deploy/,
  );
});

test("staging accepts stable and beta tags", () => {
  assert.equal(validateDeployTag("staging", "v0.4.86"), "stable");
  assert.equal(validateDeployTag("staging", "v0.4.86-beta.1"), "beta");
  assert.throws(
    () => validateDeployTag("staging", "v0.4.86-test.1"),
    /staging cannot deploy/,
  );
});

test("test accepts stable, beta, and test tags", () => {
  assert.equal(validateDeployTag("test", "v0.4.86"), "stable");
  assert.equal(validateDeployTag("test", "v0.4.86-beta.1"), "beta");
  assert.equal(validateDeployTag("test", "v0.4.86-test.1"), "test");
});

test("rejects unknown deployment environments", () => {
  assert.throws(
    () => validateDeployTag("preview", "v0.4.86-beta.1"),
    /Unknown deployment environment/,
  );
});

test("routes every named deployment through the shared policy guard", () => {
  const sharedWorkflow = readWorkflow("deploy.yml");
  assert.doesNotMatch(sharedWorkflow, /^  workflow_dispatch:/m);
  assert.match(sharedWorkflow, /SOURCE_REF_TYPE: \$\{\{ github\.ref_type \}\}/);
  assert.match(sharedWorkflow, /branches cannot be deployed/);
  assert.match(
    sharedWorkflow,
    /node scripts\/validate-deploy-tag\.mjs "\$DEPLOY_ENVIRONMENT" "\$IMAGE_TAG"/,
  );
  assert.match(
    sharedWorkflow,
    /PROJECT_OWNER_BYPASS_ENABLED: \$\{PROJECT_OWNER_BYPASS_ENABLED:-true\}/,
  );
  assert.match(
    sharedWorkflow,
    /PROJECT_OWNER_BYPASS_ENABLED mismatch: expected/,
  );

  for (const [name, environment] of [
    ["deploy-production.yml", "production"],
    ["deploy-staging.yml", "staging"],
    ["deploy-test.yml", "test"],
  ]) {
    const workflow = readWorkflow(name);
    assert.doesNotMatch(workflow, /^  workflow_call:/m);
    assert.doesNotMatch(workflow, /^    inputs:/m);
    assert.match(workflow, /uses: \.\/\.github\/workflows\/deploy\.yml/);
    assert.match(workflow, /image_tag: \$\{\{ github\.ref_name \}\}/);
    assert.match(workflow, /require_tag_ref: true/);
    assert.match(workflow, new RegExp(`environment_name: ${environment}`));
  }
});

test("does not deploy mutable latest images to test automatically", () => {
  assert.doesNotMatch(readWorkflow("deploy-test.yml"), /workflow_run:/);
});

test("runs database migrations and waits for backend readiness", () => {
  const sharedWorkflow = readWorkflow("deploy.yml");

  assert.match(
    sharedWorkflow,
    /compose run --rm --no-deps --entrypoint \.\/migrate backend up/,
  );
  assert.match(sharedWorkflow, /http:\/\/127\.0\.0\.1:8080\/readyz/);
  assert.match(sharedWorkflow, /Backend did not become ready within 180 seconds/);
});

// 2026-10-20 coder(lq): Execute the exact remote persistence block against fixtures;
// a successful deployment must not leave maintenance pointing at an old release.
function persistenceBlock() {
  const workflow = readWorkflow("deploy.yml");
  return workflow.split("# BEGIN PERSIST_VERIFIED_DEPLOY_TAG\n")[1]
    .split("# END PERSIST_VERIFIED_DEPLOY_TAG")[0]
    .split("\n").map((line) => line.replace(/^ {12}/, "")).join("\n");
}

test("persists the verified tag while preserving other settings and permissions", () => {
  for (const original of [
    "# deployment\nMULTICA_IMAGE_TAG=v0.5.5\nOTHER_SETTING=keep-exactly\n",
    "OTHER_SETTING=keep-exactly\n",
    "  export MULTICA_IMAGE_TAG = 'v0.5.5'\nMULTICA_IMAGE_TAG=v0.5.9\nOTHER_SETTING=keep-exactly\n",
  ]) {
    const dir = mkdtempSync(join(tmpdir(), "missionos-deploy-tag-"));
    try {
      writeFileSync(join(dir, ".env"), original, { mode: 0o600 });
      const result = spawnSync("sh", ["-c", `set -eu\nimage_tag=v0.5.10\n${persistenceBlock()}`], {
        cwd: dir, encoding: "utf8",
      });
      assert.equal(result.status, 0, result.stderr);
      const saved = readFileSync(join(dir, ".env"), "utf8");
      assert.equal(saved.match(/^MULTICA_IMAGE_TAG=v0\.5\.10$/gm)?.length, 1);
      assert.ok(saved.includes("OTHER_SETTING=keep-exactly\n"));
      assert.equal(saved.includes("v0.5.5"), false);
      assert.equal(statSync(join(dir, ".env")).mode & 0o777, 0o600);
      assert.deepEqual(readdirSync(dir), [".env"]);
      assert.equal(result.stdout, "");
    } finally { rmSync(dir, { recursive: true, force: true }); }
  }
});

test("verifies running images before persisting and rechecks without temporary export", () => {
  const workflow = readWorkflow("deploy.yml");
  assert.match(workflow, /group: deploy-.*inputs\.compose_project/);
  assert.match(workflow, /cancel-in-progress: false/);
  const persisted = workflow.indexOf("# BEGIN PERSIST_VERIFIED_DEPLOY_TAG");
  assert.ok(workflow.indexOf("Backend did not become ready") < persisted);
  assert.ok(workflow.indexOf("is running a stale image ID") < persisted);
  assert.ok(workflow.indexOf("PROJECT_OWNER_BYPASS_ENABLED mismatch") < persisted);
  assert.ok(workflow.indexOf("unset MULTICA_IMAGE_TAG") > persisted);
  assert.match(workflow, /compose config --images/);
});

test("never replaces a missing or symlinked environment file", () => {
  const dir = mkdtempSync(join(tmpdir(), "missionos-deploy-tag-"));
  try {
    const result = spawnSync("sh", ["-c", `set -eu\nimage_tag=v0.5.10\n${persistenceBlock()}`], {
      cwd: dir, encoding: "utf8",
    });
    assert.notEqual(result.status, 0);
    assert.deepEqual(readdirSync(dir), []);
    writeFileSync(join(dir, "original"), "MULTICA_IMAGE_TAG=v0.5.5\n", { mode: 0o600 });
    symlinkSync("original", join(dir, ".env"));
    const linked = spawnSync("sh", ["-c", `set -eu\nimage_tag=v0.5.10\n${persistenceBlock()}`], {
      cwd: dir, encoding: "utf8",
    });
    assert.notEqual(linked.status, 0);
    assert.equal(readFileSync(join(dir, "original"), "utf8"), "MULTICA_IMAGE_TAG=v0.5.5\n");
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test("a wrong tag or stale running image cannot be recorded as deployment success", () => {
  const workflow = readWorkflow("deploy.yml");
  const start = workflow.indexOf("            for service in backend frontend; do");
  const end = workflow.indexOf("            # 2026-10-20 coder(lq): Persist only", start);
  assert.ok(start >= 0 && end > start);
  const verification = workflow.slice(start, end);
  for (const scenario of ["correct", "wrong-tag", "stale-image"]) {
    const dir = mkdtempSync(join(tmpdir(), "missionos-deploy-images-"));
    try {
      writeFileSync(join(dir, ".env"), "MULTICA_IMAGE_TAG=v0.5.5\n", { mode: 0o600 });
      const result = spawnSync("sh", ["-c", `set -eu
image_tag=v0.5.10
scenario=${scenario}
compose() { printf '%s\\n' "$3"; }
docker() {
  if [ "$1" = image ]; then printf '%s\\n' expected-id; return; fi
  case "$3" in
    *Config.Image*)
      if [ "$scenario" = wrong-tag ]; then printf '%s\\n' registry/multica-backend:v0.5.5;
      else printf '%s\\n' registry/multica-backend:v0.5.10; fi ;;
    *Image*)
      if [ "$scenario" = stale-image ]; then printf '%s\\n' old-id;
      else printf '%s\\n' expected-id; fi ;;
  esac
}
${verification}
${persistenceBlock()}`], { cwd: dir, encoding: "utf8" });
      assert.equal(result.status === 0, scenario === "correct", result.stderr);
      assert.equal(readFileSync(join(dir, ".env"), "utf8"),
        `MULTICA_IMAGE_TAG=${scenario === "correct" ? "v0.5.10" : "v0.5.5"}\n`);
    } finally { rmSync(dir, { recursive: true, force: true }); }
  }
});
