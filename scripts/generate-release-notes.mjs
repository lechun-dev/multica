#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const STABLE_TAG_PATTERN = /^v(\d+)\.(\d+)\.(\d+)$/;

const CATEGORIES = [
  ["feat", "新功能"],
  ["fix", "问题修复"],
  ["perf", "性能优化"],
  ["refactor", "代码改进"],
  ["build", "构建"],
  ["ci", "发布与持续集成"],
  ["docs", "文档"],
  ["test", "测试"],
  ["chore", "维护"],
  ["revert", "回滚"],
  ["other", "其他变更"],
];

function stableVersion(tag) {
  const match = STABLE_TAG_PATTERN.exec(tag);
  return match ? match.slice(1).map(Number) : null;
}

function compareVersions(left, right) {
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) return left[index] - right[index];
  }
  return 0;
}

export function previousStableTag(tags, currentTag) {
  const currentVersion = stableVersion(currentTag);
  if (!currentVersion) {
    throw new Error(
      `Production release notes require a stable vX.Y.Z tag; got "${currentTag}".`,
    );
  }

  // 2026-09-14 coder(lq): Compare semantic version components instead of tag
  // text so v0.4.10 correctly follows v0.4.9, while prerelease tags are ignored.
  return (
    tags
      .map((tag) => ({ tag, version: stableVersion(tag) }))
      .filter(
        ({ tag, version }) =>
          tag !== currentTag &&
          version !== null &&
          compareVersions(version, currentVersion) < 0,
      )
      .sort((left, right) => compareVersions(right.version, left.version))[0]
      ?.tag ?? null
  );
}

function categoryForSubject(subject) {
  const type = /^([a-z]+)(?:\([^)]+\))?!?:\s/.exec(subject)?.[1];
  return CATEGORIES.some(([name]) => name === type) ? type : "other";
}

function escapeMarkdown(value) {
  return value.replace(/([\\[\]*_])/g, "\\$1");
}

function commitBullet(commit, repository) {
  const shortHash = commit.hash.slice(0, 7);
  const codeHash = "`" + shortHash + "`";
  const reference = repository
    ? `[${codeHash}](https://github.com/${repository}/commit/${commit.hash})`
    : codeHash;
  return `- ${reference} ${escapeMarkdown(commit.subject)}`;
}

export function formatReleaseNotes({
  tag,
  previousTag,
  commits,
  repository,
}) {
  const rangeLabel = previousTag ? `${previousTag}...${tag}` : tag;
  const lines = [
    "## 更新内容",
    "",
    `> 自动汇总 \`${rangeLabel}\` 范围内的 ${commits.length} 个非合并提交。`,
  ];

  const grouped = new Map(CATEGORIES.map(([name]) => [name, []]));
  for (const commit of commits) {
    grouped.get(categoryForSubject(commit.subject)).push(commit);
  }

  for (const [name, label] of CATEGORIES) {
    const categoryCommits = grouped.get(name);
    if (categoryCommits.length === 0) continue;
    lines.push("", `### ${label}`);
    lines.push(...categoryCommits.map((commit) => commitBullet(commit, repository)));
  }

  if (commits.length === 0) {
    lines.push("", "此版本与上一个正式版之间没有新的非合并提交。");
  }

  if (repository && previousTag) {
    lines.push(
      "",
      `**完整变更**: [${rangeLabel}](https://github.com/${repository}/compare/${previousTag}...${tag})`,
    );
  }

  return `${lines.join("\n")}\n`;
}

function runGit(args, repoRoot) {
  return execFileSync("git", args, {
    cwd: repoRoot,
    encoding: "utf8",
  }).trim();
}

function commitsInRange(repoRoot, previousTag, tag) {
  const range = previousTag ? `${previousTag}..${tag}` : tag;
  const output = runGit(
    ["log", "--no-merges", "--format=%H%x09%s", range],
    repoRoot,
  );
  if (!output) return [];

  return output.split("\n").map((line) => {
    const separator = line.indexOf("\t");
    return {
      hash: line.slice(0, separator),
      subject: line.slice(separator + 1),
    };
  });
}

function validateRepository(value) {
  return /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(value ?? "")
    ? value
    : null;
}

function main() {
  const tag = process.argv[2] ?? process.env.GITHUB_REF_NAME;
  const outputPath = process.argv[3];
  const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

  if (!tag || !outputPath) {
    throw new Error(
      "Usage: node scripts/generate-release-notes.mjs <stable-tag> <output-file>",
    );
  }

  // 2026-09-14 coder(lq): Limit candidates to tags reachable from the release
  // commit so an unrelated branch cannot become the production comparison base.
  const mergedTags = runGit(
    ["tag", "--merged", `${tag}^{}`, "--list", "v*"],
    repoRoot,
  )
    .split("\n")
    .filter(Boolean);
  const previousTag = previousStableTag(mergedTags, tag);
  const commits = commitsInRange(repoRoot, previousTag, tag);
  const notes = formatReleaseNotes({
    tag,
    previousTag,
    commits,
    repository: validateRepository(process.env.GITHUB_REPOSITORY),
  });

  writeFileSync(resolve(outputPath), notes, "utf8");
  console.log(
    `Generated ${tag} release notes from ${previousTag ?? "repository history"}: ${commits.length} commits.`,
  );
}

const isCli =
  process.argv[1] &&
  fileURLToPath(import.meta.url) === resolve(process.argv[1]);

if (isCli) {
  try {
    main();
  } catch (error) {
    console.error(`::error::${error.message}`);
    process.exitCode = 1;
  }
}
