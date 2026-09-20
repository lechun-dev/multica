import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
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
