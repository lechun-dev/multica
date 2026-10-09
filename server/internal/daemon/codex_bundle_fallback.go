package daemon

import (
	"context"
	"os"
	"strings"
	"time"
)

func automaticCodexBundleEntry(provider, command, path string) bool {
	if provider != "codex" || command != "codex" || strings.TrimSpace(os.Getenv("MULTICA_CODEX_PATH")) != "" {
		return false
	}
	for _, candidate := range codexDesktopAppBundlePaths() {
		if path == candidate {
			return true
		}
	}
	return false
}

// 2026-10-09 coder(lq): An existing executable can still be broken or too old.
// Only automatically discovered bundles may fall through to another install;
// adoption verifies the minimum version and publishes the path/version pair.
func (d *Daemon) adoptAutomaticCodexFallback(ctx context.Context, failedPath string) healOutcome {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	seen := map[string]bool{failedPath: true}
	try := func(path string) healOutcome {
		if seen[path] || ctx.Err() != nil || !agentExecutablePresent(path) {
			return healOutcome{}
		}
		seen[path] = true
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return d.adoptAgentPath(probeCtx, "codex", "codex", path, "automatic bundle failed; trying compatible fallback")
	}
	for _, path := range codexDesktopAppBundlePaths() {
		if outcome := try(path); outcome.adopted.path != "" {
			return outcome
		}
	}
	if path, err := resolveAgentExecutablePath("codex"); err == nil {
		if outcome := try(path); outcome.adopted.path != "" {
			return outcome
		}
	}
	if ctx.Err() == nil {
		if path, ok := cachedShellResolvedAgents()["codex"]; ok {
			return try(path)
		}
	}
	return healOutcome{}
}
