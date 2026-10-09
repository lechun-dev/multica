package agent

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

type dshDesktopConnection struct {
	BaseURL   string `yaml:"baseURL"`
	APIKeyEnv string `yaml:"apiKeyEnv"`
}

var dshCredentialReference = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// 2026-10-09 coder(lq): Desktop form edits are profile-local. Share only its endpoint/reference and model route through official --patch; DSH resolves the secret itself.
func prepareDshLaunch(env []string, cwd string) ([]string, func(), error) {
	cleanup := func() {}
	lookup := func(key string) string {
		value := ""
		for _, entry := range env {
			name, candidate, ok := strings.Cut(entry, "=")
			if ok && (name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key))) {
				value = candidate
			}
		}
		return value
	}
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	home := lookup(homeKey)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	root := lookup("DSH_HOME")
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(home, ".dsh")
	} else if root == "~" {
		root = home
	} else if strings.HasPrefix(root, "~/") || strings.HasPrefix(root, `~\`) {
		root = filepath.Join(home, root[2:])
	}
	if !filepath.IsAbs(root) && cwd != "" {
		root = filepath.Join(cwd, root)
	}
	file, err := os.Open(filepath.Join(root, "profiles", "desktop", "cordis.patch.yml"))
	if os.IsNotExist(err) {
		return dshLaunchArgs(), cleanup, nil
	}
	if err != nil {
		return nil, cleanup, fmt.Errorf("cannot read official DSH desktop connection settings")
	}
	defer file.Close()
	const limit = 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return nil, cleanup, fmt.Errorf("cannot read bounded official DSH desktop connection settings")
	}
	connection, err := parseDshDesktopConnection(data)
	if err != nil {
		return dshLaunchArgs(), cleanup, err
	}
	// 2026-10-09 coder(lq): ACP explicitly overrides the shared default service. Map Desktop's route to ACP's documented provider/model fields.
	model, err := parseDshDesktopFields(data, "agent-default-model", []string{"provider", "model"})
	if err != nil {
		return nil, cleanup, err
	}
	for _, value := range model {
		if strings.TrimSpace(value) == "" {
			return nil, cleanup, fmt.Errorf("official DSH desktop default model fields must not be empty")
		}
	}
	type desktopPatch struct {
		ID     string `yaml:"id"`
		Config any    `yaml:"config"`
	}
	var patch []desktopPatch
	if connection != nil {
		patch = append(patch, desktopPatch{ID: "llm-deepseek", Config: connection})
	}
	if len(model) > 0 {
		patch = append(patch, desktopPatch{ID: "acp", Config: model})
	}
	if len(patch) == 0 {
		return dshLaunchArgs(), cleanup, nil
	}
	payload, err := yaml.Marshal(patch)
	if err != nil {
		return nil, cleanup, fmt.Errorf("cannot encode official DSH desktop connection")
	}
	overlay, err := os.CreateTemp("", "missionos-dsh-connection-*.yml")
	if err != nil {
		return nil, cleanup, fmt.Errorf("cannot prepare private DSH connection overlay")
	}
	cleanup = func() { _ = os.Remove(overlay.Name()) }
	_, writeErr := overlay.Write(payload)
	closeErr := overlay.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("cannot write private DSH connection overlay")
	}
	return append(dshLaunchArgs(), "--patch", overlay.Name()), cleanup, nil
}

func parseDshDesktopFields(data []byte, plugin string, fields []string) (map[string]string, error) {
	var patches []struct {
		ID     string               `yaml:"id"`
		Config map[string]yaml.Node `yaml:"config"`
	}
	if err := yaml.Unmarshal(data, &patches); err != nil {
		// 2026-10-08 coder(lq): YAML parser errors may quote secrets or unrelated App settings; never return their raw diagnostics.
		return nil, fmt.Errorf("invalid official DSH desktop connection settings")
	}
	values := map[string]string{}
	for _, patch := range patches {
		if patch.ID != plugin {
			continue
		}
		for _, field := range fields {
			node, ok := patch.Config[field]
			if !ok {
				continue
			}
			if node.Tag == "!!null" {
				delete(values, field)
				continue
			}
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
				return nil, fmt.Errorf("official DSH desktop connection fields must be literal strings")
			}
			values[field] = node.Value
		}
	}
	return values, nil
}

func parseDshDesktopConnection(data []byte) (*dshDesktopConnection, error) {
	values, err := parseDshDesktopFields(data, "llm-deepseek", []string{"baseURL", "apiKeyEnv"})
	if err != nil {
		return nil, err
	}
	baseURL, hasURL := values["baseURL"]
	if !hasURL {
		if _, hasRef := values["apiKeyEnv"]; hasRef {
			return nil, fmt.Errorf("configure an explicit official DSH desktop API URL before sharing its custom credential reference")
		}
		return nil, nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("official DSH desktop API URL must be HTTP(S) without embedded credentials, query or fragment")
	}
	ref := "DEEPSEEK_API_KEY"
	if value, ok := values["apiKeyEnv"]; ok {
		ref = value
	}
	if !dshCredentialReference.MatchString(ref) {
		return nil, fmt.Errorf("invalid official DSH desktop credential reference")
	}
	return &dshDesktopConnection{BaseURL: baseURL, APIKeyEnv: ref}, nil
}
