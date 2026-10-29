package wscompress

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPolicyWatcherAppliesAtomicRenameAndKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	os.WriteFile(path, []byte("realtime:\n  negotiate: true\n  send: true\n  rule:\n    default: true\n"), 0600)
	t.Setenv(EnvFile, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := FromEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !c.policy().config.Realtime.Send {
		t.Fatal("initial policy missing")
	}
	os.WriteFile(path+".new", []byte("level: 1\n"), 0600)
	os.Rename(path+".new", path)
	deadline := time.Now().Add(5 * time.Second)
	for c.policy().config.Realtime.Send {
		if time.Now().After(deadline) {
			t.Fatal("hot rollback exceeded 5 seconds")
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := ReloadErrors.Load()
	os.WriteFile(path, []byte("level: invalid"), 0600)
	deadline = time.Now().Add(5 * time.Second)
	for ReloadErrors.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("invalid reload not detected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.policy().config.Realtime.Send || c.policy().config.Level != 1 {
		t.Fatal("invalid policy replaced last good policy")
	}
}
func TestTargetingAndSnapshotOwnership(t *testing.T) {
	config := enabledConfig()
	config.Realtime.Rule = Rule{Allow: []string{"allowed"}, Deny: []string{"denied"}}
	c, _ := New(config)
	config.Realtime.Rule.Allow[0] = "changed"
	s := c.policy()
	if !s.eligible(Realtime, "allowed", "w") || s.eligible(Realtime, "denied", "w") || s.eligible(Realtime, "", "w") {
		t.Fatal("targeting or ownership failed")
	}
	config = enabledConfig()
	config.Realtime.Rule = Rule{Default: true, Deny: []string{"w"}, DenyBy: "workspace_id"}
	c, _ = New(config)
	if c.policy().eligible(Realtime, "user", "") {
		t.Fatal("missing deny identity must fail closed")
	}
	config = enabledConfig()
	config.Realtime.Rule.Percent = &struct {
		Percent int    `yaml:"percent"`
		By      string `yaml:"by"`
	}{50, "user_id"}
	c, _ = New(config)
	for i := range 100 {
		user := string(rune(i + 100))
		want := c.policy().eligible(Realtime, user, "w")
		for range 5 {
			if c.policy().eligible(Realtime, user, "w") != want {
				t.Fatal("unstable cohort")
			}
		}
	}
	if c.policy().eligible(Realtime, "", "w") {
		t.Fatal("empty percent identifier accepted")
	}
}
