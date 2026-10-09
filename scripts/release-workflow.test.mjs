import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const workflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
const desktop = readFileSync(new URL("../.github/workflows/desktop-release.yml", import.meta.url), "utf8");

function metadataScript() {
  const step = workflow.match(/      - name: Validate tag name\n([\s\S]*?)(?=\n      - name:)/)?.[1];
  const script = step?.split("        run: |\n")[1];
  assert.ok(script, "release metadata validation must remain executable in isolation");
  return script.split("\n").map((line) => line.replace(/^          /, "")).join("\n");
}

function withRepo(run) {
  const cwd = mkdtempSync(join(tmpdir(), "missionos-release-policy-"));
  const git = (...args) => execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  try {
    git("init", "-b", "main");
    git("-c", "user.name=Release test", "-c", "user.email=release-test@example.invalid", "commit", "--allow-empty", "-m", "fixture");
    git("update-ref", "refs/remotes/origin/main", "HEAD");
    const output = join(cwd, "outputs");
    const validate = (tag, event = "workflow_dispatch", refType = "branch") => spawnSync("bash", ["-e", "-o", "pipefail", "-c", metadataScript()], {
      cwd,
      encoding: "utf8",
      env: { ...process.env, RELEASE_TAG: tag, GITHUB_REF_NAME: "main", GITHUB_REF_TYPE: refType, GITHUB_EVENT_NAME: event, DEFAULT_BRANCH: "main", GITHUB_OUTPUT: output },
    });
    run({ git, validate, output });
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
}

test("manual recovery is additive and publishing checkouts use the verified commit", () => {
  assert.match(workflow, /^  workflow_dispatch:\n    inputs:\n      tag:/m);
  assert.match(workflow, /RELEASE_TAG:.*inputs\.tag.*github\.ref_name/);
  assert.match(workflow, /commit_sha: \$\{\{ steps\.release_meta\.outputs\.commit_sha \}\}/);
  for (const checkout of workflow.matchAll(/uses: actions\/checkout@[^\n]+\n([\s\S]*?)(?=\n      - name:|\n  [a-z]|$)/g)) {
    assert.match(checkout[1], /ref: \$\{\{ (github\.event\.repository\.default_branch|steps\.release_meta\.outputs\.commit_sha|needs\.verify\.outputs\.commit_sha) \}\}/);
  }
  assert.doesNotMatch(workflow, /\$GITHUB_REF_NAME|\$\{GITHUB_REF_NAME\}/);
  assert.match(workflow, /ALLOW_VULN_BYPASS_FOR_TAG != env\.RELEASE_TAG/);
  assert.match(workflow, /run: go tool govulncheck \.\/\.\.\./);
  assert.doesNotMatch(workflow, /COMMIT=\$\{\{ github\.sha \}\}|type=sha,prefix=sha-/);
  assert.match(workflow, /COMMIT=\$\{\{ needs\.verify\.outputs\.commit_sha \}\}/);
});

test("manual recovery resolves an existing annotated beta tag without using main as the version", () => withRepo(({ git, validate, output }) => {
  git("-c", "user.name=Release test", "-c", "user.email=release-test@example.invalid", "tag", "-a", "v0.5.3-beta.7", "-m", "annotated preview");
  const result = validate("v0.5.3-beta.7");
  assert.equal(result.status, 0, result.stderr);
  const fields = readFileSync(output, "utf8");
  assert.match(fields, /^tag_name=v0\.5\.3-beta\.7$/m);
  assert.match(fields, /^is_stable=false$/m);
  assert.ok(fields.includes(`commit_sha=${git("rev-parse", "HEAD")}`));
}));

test("automatic tag pushes and stable tags on main retain the original release path", () => withRepo(({ git, validate, output }) => {
  git("tag", "v0.5.3");
  const result = validate("v0.5.3", "push", "tag");
  assert.equal(result.status, 0, result.stderr);
  assert.match(readFileSync(output, "utf8"), /^is_stable=true$/m);
}));

test("recovery rejects missing tags, branches, malformed input and non-tag push events", () => withRepo(({ validate }) => {
  for (const tag of ["main", "v0.5.3-beta.999", "v0.5.3;echo unsafe", "v0.5.3\nextra"]) {
    assert.notEqual(validate(tag).status, 0, tag);
  }
  assert.notEqual(validate("v0.5.3", "push", "branch").status, 0);
}));

test("stable recovery rejects a tagged commit outside main", () => withRepo(({ git, validate }) => {
  git("checkout", "-b", "fixture-other");
  git("-c", "user.name=Release test", "-c", "user.email=release-test@example.invalid", "commit", "--allow-empty", "-m", "not on main");
  git("tag", "v0.5.3");
  assert.notEqual(validate("v0.5.3").status, 0);
}));

test("desktop packaging uses a patched toolchain and scans the exact tagged source before building", () => {
  assert.match(desktop, /go-version: "1\.26\.x"\n\s+check-latest: true/);
  assert.match(desktop, /name: Scan Go vulnerabilities[\s\S]*?working-directory: server\n\s+run: go tool govulncheck \.\/\.\.\./);
  assert.ok(desktop.indexOf("name: Scan Go vulnerabilities") < desktop.indexOf("name: Build signed macOS installer"));
});
