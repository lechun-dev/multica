package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const taskCLIBinDirName = "multica-bin"

// injectTaskCLIPATH puts both `multica` and `missionos` on the agent's PATH.
//
// 2026-09-12 coder(lq): Desktop ships the CLI as missionos, but task prompts
// still tell agents to run `multica`. If LookPath("multica") fails, agents
// search the disk for tens of minutes. Stage both names in a task-local
// directory (symlink, then copy) and prepend that directory to PATH.
func injectTaskCLIPATH(agentEnv map[string]string, taskTempDir string, logger *slog.Logger) {
	if agentEnv == nil {
		return
	}
	selfBin, err := resolveSelfExecutable()
	if err != nil || strings.TrimSpace(selfBin) == "" {
		return
	}
	if !filepath.IsAbs(selfBin) {
		if abs, absErr := filepath.Abs(selfBin); absErr == nil {
			selfBin = abs
		}
	}
	inherited := os.Getenv("PATH")
	if strings.TrimSpace(taskTempDir) == "" {
		agentEnv["PATH"] = filepath.Dir(selfBin) + string(os.PathListSeparator) + inherited
		return
	}
	aliasDir, err := ensureTaskCLIBinDir(selfBin, taskTempDir)
	if err != nil {
		if logger != nil {
			logger.Warn("task CLI PATH aliases unavailable; falling back to self directory", "error", err)
		}
		agentEnv["PATH"] = filepath.Dir(selfBin) + string(os.PathListSeparator) + inherited
		return
	}
	agentEnv["PATH"] = aliasDir + string(os.PathListSeparator) + inherited
}

func taskCLIBinaryNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"multica.exe", "missionos.exe"}
	}
	return []string{"multica", "missionos"}
}

func taskCLIBinDir(taskTempDir string) string {
	return filepath.Join(taskTempDir, taskCLIBinDirName)
}

func sameFilePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// ensureTaskCLIBinDir stages both CLI names under taskTempDir/multica-bin.
func ensureTaskCLIBinDir(selfBin, taskTempDir string) (string, error) {
	destDir := taskCLIBinDir(taskTempDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	staged := 0
	var firstErr error
	for _, name := range taskCLIBinaryNames() {
		dest := filepath.Join(destDir, name)
		// 2026-09-12 coder(lq): Never Remove the running binary if destDir
		// happens to be the self directory.
		if sameFilePath(dest, selfBin) {
			staged++
			continue
		}
		if err := stageTaskCLIAlias(selfBin, dest); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		staged++
	}
	if staged == 0 {
		if firstErr != nil {
			return "", firstErr
		}
		return "", fmt.Errorf("task CLI aliases: none staged")
	}
	return destDir, nil
}

func stageTaskCLIAlias(selfBin, dest string) error {
	_ = os.Remove(dest)
	if err := os.Symlink(selfBin, dest); err == nil {
		return nil
	}
	return copyTaskCLIExecutable(selfBin, dest)
}

func copyTaskCLIExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
