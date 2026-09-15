#!/usr/bin/env node

import { pathToFileURL } from "node:url";

const TAG_PATTERNS = {
  stable: /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/,
  beta: /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)-beta\.[1-9]\d*$/,
  test: /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)-test\.[1-9]\d*$/,
};

export const DEPLOY_TAG_POLICIES = {
  production: ["stable"],
  staging: ["stable", "beta"],
  test: ["stable", "beta", "test"],
};

const TAG_EXAMPLES = {
  stable: "v0.4.86",
  beta: "v0.4.86-beta.1",
  test: "v0.4.86-test.1",
};

export function classifyDeployTag(tag) {
  return Object.entries(TAG_PATTERNS).find(([, pattern]) => pattern.test(tag))?.[0] ?? null;
}

export function validateDeployTag(environment, tag) {
  const allowedChannels = DEPLOY_TAG_POLICIES[environment];
  if (!allowedChannels) {
    throw new Error(
      `Unknown deployment environment '${environment}'. Expected production, staging, or test.`,
    );
  }

  const channel = classifyDeployTag(tag);
  if (!channel || !allowedChannels.includes(channel)) {
    const expected = allowedChannels.map((allowed) => TAG_EXAMPLES[allowed]).join(", ");
    throw new Error(
      `${environment} cannot deploy tag '${tag || "<empty>"}'. Allowed formats: ${expected}.`,
    );
  }

  return channel;
}

function main() {
  const [environment, tag] = process.argv.slice(2);
  if (!environment || !tag) {
    console.error("Usage: node scripts/validate-deploy-tag.mjs <environment> <image-tag>");
    process.exitCode = 2;
    return;
  }

  try {
    const channel = validateDeployTag(environment, tag);
    console.log(`Validated ${environment} deployment tag ${tag} (${channel}).`);
  } catch (error) {
    console.error(`::error::${error.message}`);
    process.exitCode = 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
