package notify

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config contains only module-owned settings. Infrastructure and DingTalk
// values are intentionally strings so deployment can inject them later.
type Config struct {
	Mode                  string
	Enabled               bool
	AppBaseURL            string
	APIBaseURL            string
	DatabaseURL           string
	RedisURL              string
	EncryptionKey         string
	SecretStoreRef        string
	DingTalkClientID      string
	DingTalkClientSecret  string
	DingTalkCorpID        string
	DingTalkRedirectURI   string
	DingTalkAgentID       string
	DingTalkRobotCode     string
	DingTalkAPIBaseURL    string
	DingTalkOAuthAuthURL  string
	DingTalkOAuthTokenURL string
	DingTalkOAuthUserURL  string
	WorkerInterval        time.Duration
	MaxAttempts           int
}

func (c Config) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		mode = "local/mock"
	}
	if mode != "local/mock" && mode != "staging" && mode != "production" {
		return fmt.Errorf("unsupported DingTalk notification mode %q", c.Mode)
	}
	if !c.Enabled || mode == "local/mock" {
		return nil
	}
	// DATABASE_URL 由项目根配置提供；钉钉只需要应用凭证、回调地址和机器人编码。
	for name, value := range map[string]string{
		"database_url":                c.DatabaseURL,
		"dingtalk_client_id":          c.DingTalkClientID,
		"dingtalk_client_secret":      c.DingTalkClientSecret,
		"dingtalk_oauth_redirect_uri": c.DingTalkRedirectURI,
		"dingtalk_robot_code":         c.DingTalkRobotCode,
	} {
		if strings.TrimSpace(value) == "" || strings.Contains(value, "CHANGE_ME") {
			return fmt.Errorf("%s must be configured when notifications are enabled", name)
		}
	}
	return nil
}

var ErrDisabled = errors.New("DingTalk notifications are disabled")
