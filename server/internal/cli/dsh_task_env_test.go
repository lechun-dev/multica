package cli

import (
	"testing"
)

func TestDshTaskToken(t *testing.T) {
	root := t.TempDir()
	valid := map[string]string{
		"MULTICA_TOKEN": "", "DSH_SHELL": "1", "DSH_MULTICA_TOKEN": "mat_fixture",
		"MULTICA_TASK_ID": "task-test", "MULTICA_AGENT_ID": "agent-test", "MULTICA_WORKSPACE_ID": "workspace-test", TaskConfigRootEnv: root,
		"DSH_MULTICA_TASK_ID": "task-test", "DSH_MULTICA_AGENT_ID": "agent-test", "DSH_MULTICA_WORKSPACE_ID": "workspace-test", "DSH_MULTICA_TASK_CONFIG_ROOT": root,
	}
	for _, tc := range []struct {
		name, key, value string
		want             bool
	}{
		{name: "valid", want: true},
		{name: "matching primary", key: "MULTICA_TOKEN", value: "mat_fixture", want: true},
		{name: "owner credential", key: "DSH_MULTICA_TOKEN", value: "mul_owner"},
		{name: "bare prefix", key: "DSH_MULTICA_TOKEN", value: "mat_"},
		{name: "whitespace credential", key: "DSH_MULTICA_TOKEN", value: "mat_fixture\n"},
		{name: "not a shell", key: "DSH_SHELL", value: ""},
		{name: "task mismatch", key: "MULTICA_TASK_ID", value: "other-task"},
		{name: "agent mismatch", key: "DSH_MULTICA_AGENT_ID", value: "other-agent"},
		{name: "workspace mismatch", key: "DSH_MULTICA_WORKSPACE_ID", value: "other-workspace"},
		{name: "config mismatch", key: "DSH_MULTICA_TASK_CONFIG_ROOT", value: "other-root"},
		{name: "missing task", key: "MULTICA_TASK_ID", value: ""},
		{name: "conflicting primary", key: "MULTICA_TOKEN", value: "mat_other"},
		{name: "owner primary", key: "MULTICA_TOKEN", value: "mul_owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range valid {
				t.Setenv(key, value)
			}
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			token, present := DshTaskToken()
			if !present || (token == "mat_fixture") != tc.want || (!tc.want && token != "") {
				t.Fatal("unexpected task credential resolution")
			}
		})
	}
	t.Setenv("DSH_MULTICA_TOKEN", "")
	if token, present := DshTaskToken(); token != "" || present {
		t.Fatal("missing bridge should leave normal resolution unchanged")
	}
}
