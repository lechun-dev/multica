package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseDshDesktopConnection(t *testing.T) {
	for _, tt := range []struct {
		name, patch string
		want        *dshDesktopConnection
		invalid     bool
	}{
		{name: "missing provider", patch: "- id: ui\n  config:\n    expression: !!js SECRET_SENTINEL\n"},
		{name: "default reference", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://example.test/v1\n", want: &dshDesktopConnection{"https://example.test/v1", "DEEPSEEK_API_KEY"}},
		{name: "last field wins", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://old.test\n    apiKeyEnv: CUSTOM_KEY\n- id: llm-deepseek\n  config:\n    baseURL: https://new.test\n", want: &dshDesktopConnection{"https://new.test", "CUSTOM_KEY"}},
		{name: "null restores default", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://example.test\n    apiKeyEnv: CUSTOM_KEY\n- id: llm-deepseek\n  config:\n    apiKeyEnv: null\n", want: &dshDesktopConnection{"https://example.test", "DEEPSEEK_API_KEY"}},
		{name: "malformed", patch: "SECRET_SENTINEL: [", invalid: true},
		{name: "expression", patch: "- id: llm-deepseek\n  config:\n    baseURL: !!js SECRET_SENTINEL\n", invalid: true},
		{name: "non string", patch: "- id: llm-deepseek\n  config:\n    baseURL: 42\n", invalid: true},
		{name: "alias", patch: "- id: ui\n  config:\n    endpoint: &endpoint https://example.test\n- id: llm-deepseek\n  config:\n    baseURL: *endpoint\n", invalid: true},
		{name: "embedded credential", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://user:SECRET_SENTINEL@example.test\n", invalid: true},
		{name: "query", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://example.test/?key=SECRET_SENTINEL\n", invalid: true},
		{name: "fragment", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://example.test/#SECRET_SENTINEL\n", invalid: true},
		{name: "unsupported scheme", patch: "- id: llm-deepseek\n  config:\n    baseURL: file:///SECRET_SENTINEL\n", invalid: true},
		{name: "invalid reference", patch: "- id: llm-deepseek\n  config:\n    baseURL: https://example.test\n    apiKeyEnv: SECRET_SENTINEL-invalid\n", invalid: true},
		{name: "reference without endpoint", patch: "- id: llm-deepseek\n  config:\n    apiKeyEnv: SECRET_SENTINEL\n", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDshDesktopConnection([]byte(tt.patch))
			if (err != nil) != tt.invalid || (!tt.invalid && !reflect.DeepEqual(got, tt.want)) {
				t.Fatalf("unexpected connection/validation outcome: invalid=%v error=%v", tt.invalid, err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET_SENTINEL") {
				t.Fatal("configuration error disclosed input")
			}
		})
	}
}

func writeDshDesktopPatch(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, "profiles", "desktop")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cordis.patch.yml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDshLaunch(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	for _, root := range []string{filepath.Join(home, ".dsh"), "~/.dsh", "relative-dsh"} {
		t.Run(root, func(t *testing.T) {
			expanded := root
			if strings.HasPrefix(root, "~/") {
				expanded = filepath.Join(home, root[2:])
			} else if !filepath.IsAbs(root) {
				expanded = filepath.Join(cwd, root)
			}
			writeDshDesktopPatch(t, expanded, "- id: llm-deepseek\n  config:\n    baseURL: https://example.test/v1\n")
			args, cleanup, err := prepareDshLaunch([]string{homeKey + "=" + home, "DSH_HOME=" + root}, cwd)
			defer cleanup()
			if err != nil || len(args) != 4 || args[2] != "--patch" {
				t.Fatalf("launch arguments missing overlay: %v", err)
			}
			info, err := os.Stat(args[3])
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
				t.Fatal("overlay must be private")
			}
			cleanup()
			if _, err := os.Stat(args[3]); !os.IsNotExist(err) {
				t.Fatal("overlay remains after cleanup")
			}
		})
	}
	args, cleanup, err := prepareDshLaunch([]string{"DSH_HOME=" + t.TempDir()}, cwd)
	cleanup()
	if err != nil || !reflect.DeepEqual(args, dshLaunchArgs()) {
		t.Fatal("missing Desktop patch should retain official ACP defaults")
	}
	oversize := t.TempDir()
	writeDshDesktopPatch(t, oversize, strings.Repeat("x", 1024*1024+1))
	_, cleanup, err = prepareDshLaunch([]string{"DSH_HOME=" + oversize}, cwd)
	cleanup()
	if err == nil {
		t.Fatal("unbounded Desktop patch accepted")
	}
}

func TestDshDiscoveryReusesDesktopConnection(t *testing.T) {
	bin := writeDshFixture(t, fakeDshACP)
	home, record := t.TempDir(), filepath.Join(t.TempDir(), "connection.yml")
	writeDshDesktopPatch(t, home, "- id: llm-deepseek\n  config:\n    baseURL: https://configured.example.test/v1\n")
	t.Setenv("DSH_HOME", home)
	t.Setenv("DSH_TEST_PATCH_RECORD", record)
	if _, err := discoverDshModels(context.Background(), Command{Path: bin}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil || !strings.Contains(string(data), "https://configured.example.test/v1") {
		t.Fatal("discovery did not share the Desktop connection")
	}
	path, err := os.ReadFile(record + ".path")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatal("discovery leaked its connection overlay")
	}
}

func TestDshFailedSpawnCleansConnection(t *testing.T) {
	home, temp := t.TempDir(), t.TempDir()
	writeDshDesktopPatch(t, home, "- id: llm-deepseek\n  config:\n    baseURL: https://example.test/v1\n")
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	b, err := New("dsh", Config{ExecutablePath: writeDshFixture(t, fakeDshACP), Env: map[string]string{"DSH_HOME": home}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "test", ExecOptions{Cwd: filepath.Join(t.TempDir(), "missing-cwd")}); err == nil || !strings.Contains(err.Error(), "start dsh:") {
		t.Fatal("expected failure after overlay preparation and before process start")
	}
	files, err := filepath.Glob(filepath.Join(temp, "missionos-dsh-connection-*.yml"))
	if err != nil || len(files) != 0 {
		t.Fatal("failed startup leaked its connection overlay")
	}
}
