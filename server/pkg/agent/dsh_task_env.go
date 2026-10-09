package agent

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed dsh_task_env.mjs
var dshTaskEnvironmentPlugin []byte

var dshTaskIdentityKeys = []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_WORKSPACE_ID", "MULTICA_TASK_CONFIG_ROOT"}

// 2026-10-09 coder(lq): Use DSH's per-call registry rather than disabling its secret scrub or persisting task credentials.
func prepareDshTaskEnvironment(args []string, explicit map[string]string) ([]string, func(), error) {
	cleanup := func() {}
	managed := false
	for _, key := range dshTaskIdentityKeys {
		if explicit[key] != "" {
			managed = true
		}
	}
	if !managed {
		return args, cleanup, nil
	}
	expected := make(map[string]string, len(dshTaskIdentityKeys))
	for _, key := range dshTaskIdentityKeys {
		value := explicit[key]
		if value == "" || value != strings.TrimSpace(value) {
			return nil, cleanup, fmt.Errorf("DSH task environment requires %s", key)
		}
		expected[key] = value
	}
	token := explicit["MULTICA_TOKEN"]
	if !strings.HasPrefix(token, "mat_") || len(token) <= len("mat_") || strings.ContainsAny(token, " \t\r\n") {
		return nil, cleanup, fmt.Errorf("DSH task environment requires a task-scoped mat_ credential")
	}
	if !filepath.IsAbs(expected["MULTICA_TASK_CONFIG_ROOT"]) {
		return nil, cleanup, fmt.Errorf("DSH task environment requires an absolute task config root")
	}
	dir, err := os.MkdirTemp("", "missionos-dsh-task-*")
	if err != nil {
		return nil, cleanup, fmt.Errorf("cannot prepare private DSH task environment")
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	fail := func() ([]string, func(), error) {
		cleanup()
		return nil, func() {}, fmt.Errorf("cannot write private DSH task environment")
	}
	if err := os.WriteFile(filepath.Join(dir, "task-env.mjs"), dshTaskEnvironmentPlugin, 0600); err != nil {
		return fail()
	}
	patches := []map[string]any{{"insert": []map[string]any{{"id": "missionos-task-env", "name": "./task-env.mjs", "config": expected}}}}
	payload, err := yaml.Marshal(patches)
	if err != nil {
		return fail()
	}
	patchPath := filepath.Join(dir, "cordis.patch.yml")
	if err := os.WriteFile(patchPath, payload, 0600); err != nil {
		return fail()
	}
	return append(append([]string(nil), args...), "--patch", patchPath), cleanup, nil
}
