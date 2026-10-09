package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// 2026-10-09 coder(lq): Default tests must never execute user-installed app
// bundles. Bundle tests explicitly supply isolated temporary CLI candidates.
func init() { codexDesktopAppBundlePaths = func() []string { return nil } }

func setupCodexBundleFallback(t *testing.T) (string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("macOS bundle discovery uses POSIX executable fixtures")
	}
	root := t.TempDir()
	nested := filepath.Join(root, "ChatGPT.app", "nested", "codex")
	flat := filepath.Join(root, "ChatGPT.app", "flat", "codex")
	pathCLI := filepath.Join(root, "bin", "codex")
	for _, path := range []string{nested, flat, pathCLI} {
		writeExecStub(t, path)
	}
	previous := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{nested, flat} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = previous })
	t.Setenv("MULTICA_CODEX_PATH", "")
	t.Setenv("PATH", filepath.Dir(pathCLI))
	t.Setenv("SHELL", filepath.Join(root, "fish"))
	return nested, flat, pathCLI
}

func TestAutomaticCodexDiscoveryPrefersBundleAndHonorsExplicitOverrides(t *testing.T) {
	nested, flat, pathCLI := setupCodexBundleFallback(t)
	assertPath := func(want string) {
		t.Helper()
		if got := probeAgentCLIs()["codex"].Path; got != want {
			t.Fatalf("discovered %q want %q", got, want)
		}
	}
	assertPath(nested)
	if got, ok := reresolveAgentCommand("codex", "codex"); !ok || got != nested {
		t.Fatalf("repair path=%q ok=%v", got, ok)
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	assertPath(flat)
	if err := os.Chmod(flat, 0644); err != nil {
		t.Fatal(err)
	}
	assertPath(canonicalExecutablePath(pathCLI))
	t.Setenv("MULTICA_CODEX_PATH", pathCLI)
	assertPath(canonicalConfiguredExecutablePath(pathCLI))
	t.Setenv("MULTICA_CODEX_PATH", "codex")
	assertPath(canonicalExecutablePath(pathCLI))
	t.Setenv("MULTICA_CODEX_PATH", filepath.Join(t.TempDir(), "missing"))
	if _, ok := probeAgentCLIs()["codex"]; ok {
		t.Fatal("missing explicit path fell back")
	}
}

func TestAutomaticCodexBrokenBundleFallsBackAndPairsVersion(t *testing.T) {
	for _, kind := range []string{"broken", "too-old", "unparseable"} {
		t.Run(kind, func(t *testing.T) {
			nested, flat, _ := setupCodexBundleFallback(t)
			original := detectAgentVersion
			detectAgentVersion = func(_ context.Context, cmd agent.Command) (string, error) {
				if cmd.Path == nested {
					switch kind {
					case "broken":
						return "", fmt.Errorf("broken executable")
					case "too-old":
						return "0.1.0", nil
					default:
						return "unparseable", nil
					}
				}
				if cmd.Path == flat {
					return "0.162.0", nil
				}
				return "", fmt.Errorf("unexpected candidate %s", cmd.Path)
			}
			t.Cleanup(func() { detectAgentVersion = original })
			d := newSelfHealTestDaemon()
			entry := AgentEntry{Path: nested, Command: "codex"}
			version, reason, verdict := d.probeBuiltinRuntime(context.Background(), "codex", entry)
			if verdict != builtinProbeOK || version != "0.162.0" {
				t.Fatalf("verdict=%v version=%q reason=%s", verdict, version, reason)
			}
			resolved, pairedVersion, err := d.resolveAgentEntryForLaunch(context.Background(), "codex", entry)
			if err != nil || resolved.Path != flat || pairedVersion != version {
				t.Fatalf("launch path=%s version=%s err=%v", resolved.Path, pairedVersion, err)
			}
		})
	}
}

func TestAutomaticCodexBrokenBundlesFallBackToPATH(t *testing.T) {
	nested, _, pathCLI := setupCodexBundleFallback(t)
	original := detectAgentVersion
	detectAgentVersion = func(_ context.Context, cmd agent.Command) (string, error) {
		if cmd.Path == canonicalExecutablePath(pathCLI) {
			return "0.150.1", nil
		}
		return "", fmt.Errorf("broken bundle")
	}
	t.Cleanup(func() { detectAgentVersion = original })
	d := newSelfHealTestDaemon()
	entry := AgentEntry{Path: nested, Command: "codex"}
	version, _, verdict := d.probeBuiltinRuntime(context.Background(), "codex", entry)
	got, paired := d.resolveAgentEntry(context.Background(), "codex", entry)
	if verdict != builtinProbeOK || version != "0.150.1" || got.Path != canonicalExecutablePath(pathCLI) || paired != version {
		t.Fatalf("verdict=%v path=%s version=%s paired=%s", verdict, got.Path, version, paired)
	}
}

func TestAutomaticCodexLiveRepairSkipsBrokenBundle(t *testing.T) {
	_, flat, _ := setupCodexBundleFallback(t)
	original := detectAgentVersion
	detectAgentVersion = func(_ context.Context, cmd agent.Command) (string, error) {
		if cmd.Path == flat {
			return "0.162.0", nil
		}
		return "", fmt.Errorf("broken bundle")
	}
	t.Cleanup(func() { detectAgentVersion = original })
	d := newSelfHealTestDaemon()
	entry := AgentEntry{Path: filepath.Join(t.TempDir(), "vanished"), Command: "codex"}
	got, version := d.resolveAgentEntry(context.Background(), "codex", entry)
	if got.Path != flat || version != "0.162.0" {
		t.Fatalf("path=%s version=%s", got.Path, version)
	}
}

func TestAutomaticCodexFallbackNeverChangesExplicitBundle(t *testing.T) {
	nested, _, _ := setupCodexBundleFallback(t)
	t.Setenv("MULTICA_CODEX_PATH", nested)
	original := detectAgentVersion
	detectAgentVersion = func(_ context.Context, cmd agent.Command) (string, error) {
		if cmd.Path != nested {
			t.Fatalf("explicit path switched to %s", cmd.Path)
		}
		return "", fmt.Errorf("explicit binary broken")
	}
	t.Cleanup(func() { detectAgentVersion = original })
	d := newSelfHealTestDaemon()
	_, _, verdict := d.probeBuiltinRuntime(context.Background(), "codex", AgentEntry{Path: nested, Command: nested})
	if verdict == builtinProbeOK {
		t.Fatal("broken explicit bundle silently repaired")
	}
}

func TestAutomaticCodexFallbackRespectsCancellation(t *testing.T) {
	nested, _, _ := setupCodexBundleFallback(t)
	original := detectAgentVersion
	detectAgentVersion = func(ctx context.Context, cmd agent.Command) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	t.Cleanup(func() { detectAgentVersion = original })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if outcome := newSelfHealTestDaemon().adoptAutomaticCodexFallback(ctx, nested); outcome.adopted.path != "" {
		t.Fatal("adopted canceled probe")
	}
}

func TestAutomaticCodexFailedFallbackPreservesVerdictAndPath(t *testing.T) {
	nested, _, _ := setupCodexBundleFallback(t)
	original := detectAgentVersion
	detectAgentVersion = func(_ context.Context, cmd agent.Command) (string, error) {
		if cmd.Path == nested {
			return "0.1.0", nil
		}
		return "", fmt.Errorf("fallback unavailable")
	}
	t.Cleanup(func() { detectAgentVersion = original })
	d := newSelfHealTestDaemon()
	entry := AgentEntry{Path: nested, Command: "codex"}
	version, _, verdict := d.probeBuiltinRuntime(context.Background(), "codex", entry)
	if verdict != builtinProbeBelowMinimum || version != "0.1.0" {
		t.Fatalf("verdict=%v version=%s", verdict, version)
	}
	resolved, _ := d.resolveAgentEntry(context.Background(), "codex", entry)
	if resolved.Path != nested || len(d.resolvedPaths) != 0 {
		t.Fatalf("failed fallback changed launch path to %s", resolved.Path)
	}
}
