// Package wscompress implements opt-in RFC 7692 transport compression.
// It never changes business payloads or logs message contents.
package wscompress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/featureflag"
	"gopkg.in/yaml.v3"
)

const EnvFile = "MULTICA_WS_COMPRESSION_FILE"

type Endpoint string

const (
	Realtime Endpoint = "realtime"
	Daemon   Endpoint = "daemon"
)

// Rule uses the existing featureflag evaluator and stable bucketing. Only
// authenticated identity (or the server's configured instance) is supplied.
type Rule struct {
	Default bool     `yaml:"default"`
	Allow   []string `yaml:"allow"`
	AllowBy string   `yaml:"allow_by"`
	Deny    []string `yaml:"deny"`
	DenyBy  string   `yaml:"deny_by"`
	Percent *struct {
		Percent int    `yaml:"percent"`
		By      string `yaml:"by"`
	} `yaml:"percent"`
}

type EndpointPolicy struct {
	Negotiate bool `yaml:"negotiate"`
	Send      bool `yaml:"send"`
	// PreAuth permits negotiation on this controlled instance before /ws
	// first-message authentication. Sending still checks the authenticated rule.
	PreAuth bool `yaml:"pre_auth"`
	Rule    Rule `yaml:"rule"`
}

type Config struct {
	Threshold int            `yaml:"threshold"`
	Level     int            `yaml:"level"`
	Instance  string         `yaml:"instance"`
	Realtime  EndpointPolicy `yaml:"realtime"`
	Daemon    EndpointPolicy `yaml:"daemon"`
}

type snapshot struct {
	config Config
	flags  *featureflag.Service
}

type Controller struct {
	current atomic.Pointer[snapshot]
}

func New(config Config) (*Controller, error) {
	c := &Controller{}
	if err := c.replace(config); err != nil {
		return nil, err
	}
	return c, nil
}

func validBy(by string) bool {
	return by == "" || by == "user_id" || by == "workspace_id" || by == "instance"
}

func (c *Controller) replace(config Config) error {
	if config.Threshold == 0 {
		config.Threshold = 1024
	}
	if config.Level == 0 {
		config.Level = 1
	}
	// Limit operational tuning to low CPU levels in this first release.
	if config.Threshold < 1 || config.Threshold > 64<<20 || config.Level < 1 || config.Level > 3 {
		return errors.New("invalid WebSocket compression threshold or level")
	}
	// Own all slices and pointers before publishing the immutable snapshot.
	cloneRule := func(r Rule) Rule {
		r.Allow = append([]string(nil), r.Allow...)
		r.Deny = append([]string(nil), r.Deny...)
		if r.Percent != nil {
			v := *r.Percent
			r.Percent = &v
		}
		return r
	}
	config.Realtime.Rule = cloneRule(config.Realtime.Rule)
	config.Daemon.Rule = cloneRule(config.Daemon.Rule)
	provider := featureflag.NewStaticProvider()
	for key, p := range map[Endpoint]EndpointPolicy{Realtime: config.Realtime, Daemon: config.Daemon} {
		r := p.Rule
		if !validBy(r.AllowBy) || !validBy(r.DenyBy) {
			return errors.New("invalid compression targeting attribute")
		}
		rule := featureflag.Rule{Default: r.Default, Allow: r.Allow, AllowBy: r.AllowBy, Deny: r.Deny, DenyBy: r.DenyBy}
		if r.Percent != nil {
			if r.Percent.Percent < 0 || r.Percent.Percent > 100 || !validBy(r.Percent.By) {
				return errors.New("invalid compression percentage")
			}
			rule.Percent = &featureflag.PercentRollout{Percent: r.Percent.Percent, By: r.Percent.By}
		}
		provider.Set(string(key), rule)
	}
	c.current.Store(&snapshot{config: config, flags: featureflag.NewService(provider)})
	return nil
}

// Reload validates a complete, bounded policy before atomic replacement. Error
// details deliberately exclude YAML parser output (it can echo file contents).
func (c *Controller) Reload(path string) error {
	return c.reload(path)
}

func (c *Controller) reload(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open WebSocket compression policy")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return errors.New("cannot read WebSocket compression policy (limit 64 KiB)")
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	var config Config
	if err := d.Decode(&config); err != nil {
		return errors.New("invalid WebSocket compression policy YAML")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("compression policy must contain one YAML document")
	}
	if err := c.replace(config); err != nil {
		return err
	}
	return nil
}

// FromEnv starts only the compression policy watcher. It does not reload any
// other feature flags. Cancellation belongs to the server lifecycle.
func FromEnv(ctx context.Context) (*Controller, error) {
	c, _ := New(Config{})
	path := os.Getenv(EnvFile)
	if path == "" {
		return c, nil
	}
	if err := c.Reload(path); err != nil {
		return nil, err
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		wasInvalid := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Small policy files are revalidated each time, so directory-bound
				// atomic renames and same-size edits both work reliably.
				err := c.reload(path)
				if err != nil {
					ReloadErrors.Add(1)
					if !wasInvalid {
						slog.Warn("WebSocket compression policy reload rejected; keeping last good policy")
					}
					wasInvalid = true
				} else {
					wasInvalid = false
				}
			}
		}
	}()
	return c, nil
}

func (s *snapshot) endpoint(endpoint Endpoint) EndpointPolicy {
	if endpoint == Daemon {
		return s.config.Daemon
	}
	return s.config.Realtime
}

func (s *snapshot) eligible(endpoint Endpoint, userID, workspaceID string) bool {
	p := s.endpoint(endpoint)
	r := p.Rule
	// A missing identity must not share an accidental percent bucket. Instance
	// targeting remains available for authenticated legacy daemon identities.
	missing := func(by string) bool {
		switch by {
		case "", "user_id":
			return userID == ""
		case "workspace_id":
			return workspaceID == ""
		case "instance":
			return s.config.Instance == ""
		}
		return true
	}
	if (len(r.Allow) > 0 && missing(r.AllowBy)) || (len(r.Deny) > 0 && missing(r.DenyBy)) {
		return false
	}
	if r.Percent != nil && missing(r.Percent.By) {
		return false
	}
	ctx := featureflag.WithEvalContext(context.Background(), featureflag.EvalContext{UserID: userID, WorkspaceID: workspaceID, Attributes: map[string]string{"instance": s.config.Instance}})
	return s.flags.IsEnabled(ctx, string(endpoint), false)
}

func (c *Controller) policy() *snapshot {
	if c == nil {
		return nil
	}
	return c.current.Load()
}
