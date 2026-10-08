package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func dshACPFixture(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture")
	}
	path := filepath.Join(t.TempDir(), "dsh")
	script := `#!/bin/sh
[ "$*" = "--profile acp" ] || exit 2
IFS= read -r line
` + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeBuiltinRuntimeDshUsesOfficialACPWithoutManifest(t *testing.T) {
	t.Setenv("DSH_HOME", t.TempDir())
	// 2026-10-08 coder(lq): A formerly configured plugin must never be invoked, even with no multica profile.
	t.Setenv("MULTICA_DSH_PROFILE_BUNDLE", "do-not-install-this")
	path := dshACPFixture(t, `printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"0.2.0-test"}}}'; cat >/dev/null`)
	d := &Daemon{agentVersions: make(map[string]string), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	version, reason, verdict := d.probeBuiltinRuntime(context.Background(), "dsh", AgentEntry{Path: path})
	if verdict != builtinProbeOK || reason != "" || version != "0.2.0-test" {
		t.Fatalf("%s %s %v", version, reason, verdict)
	}
	entries, err := os.ReadDir(os.Getenv("DSH_HOME"))
	if err != nil || len(entries) != 0 {
		t.Fatal("availability check changed DSH_HOME")
	}
}

func TestProbeBuiltinRuntimeDshFailedACPIsTransient(t *testing.T) {
	for _, body := range []string{`printf '%s\n' '{"v":1,"type":"probe","runtime":"dsh","protocol_version":1}'`, `exit 1`, `printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":99}}'`} {
		path := dshACPFixture(t, body)
		d := &Daemon{agentVersions: make(map[string]string), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		_, reason, verdict := d.probeBuiltinRuntime(context.Background(), "dsh", AgentEntry{Path: path})
		if verdict != builtinProbeUnavailable || !strings.Contains(reason, "ACP") || demotableBuiltinProbeVerdict(verdict) {
			t.Fatalf("%s %v", reason, verdict)
		}
	}
}

func TestProbeBuiltinRuntimeDshCancellationBoundsProcessTree(t *testing.T) {
	path := dshACPFixture(t, `sleep 30 & wait`)
	d := &Daemon{agentVersions: make(map[string]string), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, verdict := d.probeBuiltinRuntime(ctx, "dsh", AgentEntry{Path: path})
	if verdict != builtinProbeUnavailable || time.Since(start) > 3*time.Second {
		t.Fatalf("probe unbounded or wrong verdict %v", verdict)
	}
}

func TestProbeAgentCLIsUsesOfficialDshBundle(t *testing.T) {
	pinNonCodexAgentsToMissingPaths(t)
	t.Setenv("MULTICA_CODEX_PATH", filepath.Join(t.TempDir(), "missing-codex"))
	path := dshACPFixture(t, `exit 0`)
	original := dshDesktopAppBundlePaths
	dshDesktopAppBundlePaths = func() []string { return []string{path} }
	t.Cleanup(func() { dshDesktopAppBundlePaths = original })
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "/nonexistent/shell")
	t.Setenv("MULTICA_DSH_PATH", "")
	if got := probeAgentCLIs()["dsh"].Path; got != path {
		t.Fatalf("got %q", got)
	}
	t.Setenv("MULTICA_DSH_PATH", "/nonexistent/explicit-dsh")
	if _, ok := probeAgentCLIs()["dsh"]; ok {
		t.Fatal("explicit override silently replaced")
	}
}
