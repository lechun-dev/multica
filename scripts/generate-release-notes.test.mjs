import assert from "node:assert/strict";
import test from "node:test";

import {
  formatReleaseNotes,
  previousStableTag,
} from "./generate-release-notes.mjs";

test("selects the newest earlier stable tag by semantic version", () => {
  assert.equal(
    previousStableTag(
      ["v0.4.8", "v0.4.9", "v0.4.10-beta.2", "v0.4.10", "v0.5.0"],
      "v0.4.10",
    ),
    "v0.4.9",
  );
});

test("rejects prerelease tags as production release targets", () => {
  assert.throws(
    () => previousStableTag(["v0.4.84"], "v0.4.85-beta.1"),
    /require a stable vX\.Y\.Z tag/,
  );
});

test("keeps all commit types in categorized release notes", () => {
  const commits = [
    { hash: "1111111111111111111111111111111111111111", subject: "feat: add task export" },
    { hash: "2222222222222222222222222222222222222222", subject: "fix(ui): repair queue state" },
    { hash: "3333333333333333333333333333333333333333", subject: "perf: reduce permission queries" },
    { hash: "4444444444444444444444444444444444444444", subject: "docs: update operations guide" },
    { hash: "5555555555555555555555555555555555555555", subject: "chore: refresh release contract" },
    { hash: "6666666666666666666666666666666666666666", subject: "plain commit subject" },
  ];

  const notes = formatReleaseNotes({
    tag: "v0.4.85",
    previousTag: "v0.4.84",
    commits,
    repository: "lechun-dev/multica",
  });

  assert.match(notes, /### 新功能/);
  assert.match(notes, /### 问题修复/);
  assert.match(notes, /### 性能优化/);
  assert.match(notes, /### 文档/);
  assert.match(notes, /### 维护/);
  assert.match(notes, /### 其他变更/);
  for (const commit of commits) {
    assert.match(notes, new RegExp(commit.hash.slice(0, 7)));
    assert.match(notes, new RegExp(commit.subject.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.match(notes, /compare\/v0\.4\.84\.\.\.v0\.4\.85/);
});

test("renders an explicit message when the stable range is empty", () => {
  const notes = formatReleaseNotes({
    tag: "v1.0.1",
    previousTag: "v1.0.0",
    commits: [],
    repository: null,
  });

  assert.match(notes, /0 个非合并提交/);
  assert.match(notes, /没有新的非合并提交/);
});
