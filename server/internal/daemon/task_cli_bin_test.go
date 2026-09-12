package daemon

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func taskCLITestName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func writeTaskCLIFixture(t *testing.T, dir, name, sentinel string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(sentinel), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func silentTaskCLILogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnsureTaskCLIBinDir_MissionOSCreatesMultica(t *testing.T) {
	selfDir := t.TempDir()
	sentinel := "missionos-self-fixture\n"
	selfBin := writeTaskCLIFixture(t, selfDir, taskCLITestName("missionos"), sentinel)

	taskTempDir := t.TempDir()
	gotDir, err := ensureTaskCLIBinDir(selfBin, taskTempDir)
	if err != nil {
		t.Fatalf("ensureTaskCLIBinDir: %v", err)
	}
	wantDir := taskCLIBinDir(taskTempDir)
	if gotDir != wantDir {
		t.Fatalf("alias dir = %q, want %q", gotDir, wantDir)
	}

	for _, name := range []string{taskCLITestName("multica"), taskCLITestName("missionos")} {
		path := filepath.Join(gotDir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != sentinel {
			t.Fatalf("%s content = %q, want sentinel", name, got)
		}
	}
}

func TestEnsureTaskCLIBinDir_SkipsSelfName(t *testing.T) {
	taskTempDir := t.TempDir()
	destDir := taskCLIBinDir(taskTempDir)
	selfSentinel := "do-not-replace-self\n"
	selfBin := writeTaskCLIFixture(t, destDir, taskCLITestName("missionos"), selfSentinel)

	if _, err := ensureTaskCLIBinDir(selfBin, taskTempDir); err != nil {
		t.Fatalf("ensureTaskCLIBinDir: %v", err)
	}

	gotSelf, err := os.ReadFile(selfBin)
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	if string(gotSelf) != selfSentinel {
		t.Fatalf("running binary was replaced: got %q", gotSelf)
	}

	alias := filepath.Join(destDir, taskCLITestName("multica"))
	gotAlias, err := os.ReadFile(alias)
	if err != nil {
		t.Fatalf("read alias: %v", err)
	}
	if string(gotAlias) != selfSentinel {
		t.Fatalf("alias content = %q, want self sentinel", gotAlias)
	}
}

func TestInjectTaskCLIPATH_MissionOSSelfExposesMultica(t *testing.T) {
	original := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = original })

	sentinel := "lookpath-sentinel-missionos\n"
	selfBin := writeTaskCLIFixture(t, t.TempDir(), taskCLITestName("missionos"), sentinel)
	resolveSelfExecutable = func() (string, error) { return selfBin, nil }

	t.Setenv("PATH", t.TempDir())
	agentEnv := map[string]string{}
	injectTaskCLIPATH(agentEnv, t.TempDir(), silentTaskCLILogger())

	path := agentEnv["PATH"]
	if path == "" {
		t.Fatal("PATH was not injected")
	}
	t.Setenv("PATH", path)

	got, err := exec.LookPath(taskCLITestName("multica"))
	if err != nil {
		t.Fatalf("LookPath(multica): %v", err)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read LookPath result: %v", err)
	}
	if string(body) != sentinel {
		t.Fatalf("LookPath(multica) resolved %s with content %q, want sentinel", got, body)
	}
}

func TestInjectTaskCLIPATH_FallsBackWhenAliasDirUnavailable(t *testing.T) {
	original := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = original })

	selfDir := t.TempDir()
	selfBin := writeTaskCLIFixture(t, selfDir, taskCLITestName("missionos"), "fallback-self\n")
	resolveSelfExecutable = func() (string, error) { return selfBin, nil }

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("nope"), 0o600); err != nil {
		t.Fatalf("write blocked temp dir: %v", err)
	}

	inherited := t.TempDir()
	t.Setenv("PATH", inherited)
	agentEnv := map[string]string{}
	injectTaskCLIPATH(agentEnv, blocked, silentTaskCLILogger())

	got := agentEnv["PATH"]
	wantPrefix := selfDir + string(os.PathListSeparator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("PATH = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, inherited) {
		t.Fatalf("PATH = %q, want inherited %q", got, inherited)
	}
}
