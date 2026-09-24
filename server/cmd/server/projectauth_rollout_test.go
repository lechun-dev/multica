package main

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestProjectPermissionRolloutFromEnv(t *testing.T) {
	tests := []struct {
		name          string
		phase         string
		legacyEnabled string
		want          projectauth.RolloutPhase
	}{
		{name: "default off", want: projectauth.RolloutOff},
		{name: "legacy enabled", legacyEnabled: "true", want: projectauth.RolloutRestricted},
		{name: "explicit phase wins", phase: " reader ", legacyEnabled: "true", want: projectauth.RolloutReader},
		{name: "explicit shadow", phase: "SHADOW", want: projectauth.RolloutShadow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PROJECT_PERMISSION_ROLLOUT_PHASE", tt.phase)
			t.Setenv("PROJECT_PERMISSION_ENABLED", tt.legacyEnabled)
			if got := projectPermissionRolloutFromEnv(); got != tt.want {
				t.Fatalf("projectPermissionRolloutFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectPermissionRolloutFromEnvRejectsUnknownPhase(t *testing.T) {
	t.Setenv("PROJECT_PERMISSION_ROLLOUT_PHASE", "writeer")
	t.Setenv("PROJECT_PERMISSION_ENABLED", "true")

	defer func() {
		if recover() == nil {
			t.Fatal("projectPermissionRolloutFromEnv must reject an unknown phase")
		}
	}()
	_ = projectPermissionRolloutFromEnv()
}
