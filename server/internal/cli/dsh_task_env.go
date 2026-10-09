package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// 2026-10-09 coder(lq): DSH forwards secrets only through its official per-call registry; never discover an Owner profile when that bridge is invalid.
func DshTaskToken() (token string, present bool) {
	token = os.Getenv("DSH_MULTICA_TOKEN")
	if token == "" {
		return "", false
	}
	if os.Getenv("DSH_SHELL") != "1" || !strings.HasPrefix(token, "mat_") || len(token) <= len("mat_") || strings.ContainsAny(token, " \t\r\n") {
		return "", true
	}
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_WORKSPACE_ID", TaskConfigRootEnv} {
		value := os.Getenv(key)
		if value == "" || value != strings.TrimSpace(value) || os.Getenv("DSH_"+key) != value {
			return "", true
		}
	}
	if !filepath.IsAbs(os.Getenv(TaskConfigRootEnv)) {
		return "", true
	}
	if primary := os.Getenv("MULTICA_TOKEN"); primary != "" && primary != token {
		return "", true
	}
	return token, true
}
