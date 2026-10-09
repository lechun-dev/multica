package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func dshTaskFixture(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"MULTICA_TOKEN": "mat_fixture", "MULTICA_TASK_ID": "task-test", "MULTICA_AGENT_ID": "agent-test", "MULTICA_WORKSPACE_ID": "workspace-test", "MULTICA_TASK_CONFIG_ROOT": t.TempDir()}
}

func TestPrepareDshTaskEnvironment(t *testing.T) {
	env := dshTaskFixture(t)
	args, cleanup, err := prepareDshTaskEnvironment([]string{"--profile", "acp", "--patch", "desktop.yml"}, env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(args) != 6 || args[3] != "desktop.yml" || args[4] != "--patch" {
		t.Fatal("existing launch overlay lost")
	}
	data, err := os.ReadFile(args[5])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), env["MULTICA_TOKEN"]) {
		t.Fatal("token written to overlay")
	}
	var patches []struct {
		Insert []struct {
			Name string `yaml:"name"`
		} `yaml:"insert"`
	}
	if err := yaml.Unmarshal(data, &patches); err != nil || len(patches) != 1 || len(patches[0].Insert) != 1 {
		t.Fatal("missing plugin insertion")
	}
	plugin := filepath.Join(filepath.Dir(args[5]), patches[0].Insert[0].Name)
	if _, err := os.Stat(plugin); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(args[5])); !os.IsNotExist(err) {
		t.Fatal("task plugin remains after cleanup")
	}
}

func TestPrepareDshTaskEnvironmentRejectsIncompleteIdentity(t *testing.T) {
	for _, key := range []string{"MULTICA_TOKEN", "MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_WORKSPACE_ID", "MULTICA_TASK_CONFIG_ROOT"} {
		t.Run(key, func(t *testing.T) {
			env := dshTaskFixture(t)
			delete(env, key)
			_, cleanup, err := prepareDshTaskEnvironment(dshLaunchArgs(), env)
			defer cleanup()
			if err == nil {
				t.Fatal("incomplete task environment accepted")
			}
		})
	}
	for _, token := range []string{"mul_OWNER_SENTINEL", "mat_", "mat_fixture\n"} {
		env := dshTaskFixture(t)
		env["MULTICA_TOKEN"] = token
		_, cleanup, err := prepareDshTaskEnvironment(dshLaunchArgs(), env)
		cleanup()
		if err == nil || strings.Contains(err.Error(), "OWNER_SENTINEL") {
			t.Fatal("invalid credential accepted or disclosed")
		}
	}
	env := dshTaskFixture(t)
	env["MULTICA_TASK_CONFIG_ROOT"] = "relative"
	_, cleanup, err := prepareDshTaskEnvironment(dshLaunchArgs(), env)
	cleanup()
	if err == nil {
		t.Fatal("relative task config accepted")
	}
	args, cleanup, err := prepareDshTaskEnvironment(dshLaunchArgs(), nil)
	defer cleanup()
	if err != nil || len(args) != 2 {
		t.Fatal("standalone DSH launch changed")
	}
}

func TestDshTaskEnvironmentPlugin(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable for isolated plugin contract test")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "task-env.mjs")
	if err := os.WriteFile(plugin, dshTaskEnvironmentPlugin, 0600); err != nil {
		t.Fatal(err)
	}
	fixture := dshTaskFixture(t)
	expected := map[string]string{}
	for _, key := range dshTaskIdentityKeys {
		expected[key] = fixture[key]
	}
	encoded, _ := json.Marshal(expected)
	cmd := exec.Command(node, "--input-type=module", "-", plugin, string(encoded))
	cmd.Env = []string{"OWNER_TOKEN=owner-must-stay-scrubbed", "DSH_STALE=old-task"}
	for key, value := range fixture {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin = strings.NewReader(`
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
const plugin = await import(pathToFileURL(process.argv[2]));
const expected = JSON.parse(process.argv[3]);
assert.deepEqual(plugin.inject, ['shellEnv']);
function install(expected) {
 let contributor;
 plugin.apply({shellEnv: {register(value) {
  for (const [key, meta] of Object.entries(value.variables)) {
   assert.match(key, /^DSH_[A-Z][A-Z0-9_]*$/);
   assert.ok(meta.description.length > 0);
  }
  contributor = value;
 }}}, expected);
 return contributor;
}
const first = install(expected);
const token = process.env.MULTICA_TOKEN;
assert.ok(!JSON.stringify(first.variables).includes(token));
assert.deepEqual(first.resolve({}), {});
const collect = c => ({DSH_SHELL: '1', ...c.resolve({agent: {}})});
const snapshot = first.resolve({agent: {}});
assert.ok(Object.isFrozen(snapshot));
process.env.MULTICA_TOKEN = 'mat_second';
process.env.MULTICA_TASK_ID = 'task-second';
const second = install({...expected, MULTICA_TASK_ID: 'task-second'});
assert.equal(collect(first).DSH_MULTICA_TOKEN, token);
assert.equal(collect(second).DSH_MULTICA_TOKEN, 'mat_second');
assert.equal(collect(first).DSH_MULTICA_TASK_ID, expected.MULTICA_TASK_ID);
assert.throws(() => install(expected), /identity mismatch/);
process.env.MULTICA_TASK_ID = expected.MULTICA_TASK_ID;
process.env.MULTICA_TOKEN = 'mul_owner';
assert.throws(() => install(expected), /task-scoped credential/);
process.env.MULTICA_TOKEN = token;
// 2026-10-09 coder(lq): Mirror DSH's documented subprocess seam: scrub inherited secrets/DSH facts, then merge the registry snapshot.
const ambient = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/KEY|PASSWORD|SECRET|TOKEN/i.test(key) && !/^DSH_/i.test(key)));
const childScript = [
 "import assert from 'node:assert/strict';",
 "assert.equal(process.env.MULTICA_TOKEN, undefined);",
 "assert.equal(process.env.OWNER_TOKEN, undefined);",
 "assert.equal(process.env.DSH_STALE, undefined);",
 "assert.equal(process.env.DSH_MULTICA_TOKEN, 'mat_fixture');",
 "assert.equal(process.env.DSH_MULTICA_TASK_ID, process.env.MULTICA_TASK_ID);",
].join('\n');
const child = spawnSync(process.execPath, ['--input-type=module', '-e', childScript], {env: {...ambient, ...collect(first)}, encoding: 'utf8'});
assert.equal(child.status, 0, child.stderr);
`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated plugin contract failed: %v\n%s", err, out)
	}
}
