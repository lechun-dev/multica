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
	AppBaseURL           string
	APIBaseURL           string
	DatabaseURL          string
	RedisURL             string
	EncryptionKey        string
	SecretStoreRef       string
	DingTalkClientID     string
	DingTalkClientSecret string
	DingTalkCorpID       string
	DingTalkRedirectURI  string
	DingTalkAgentID      string
	DingTalkRobotCode    string
	DingTalkAPIBaseURL   string
	DingTalkOAuthAuthURL string
	WorkerInterval       time.Duration
	MaxAttempts          int
}

// MissingNotificationSettings reports the application credentials required by
// the member P2P sender. Notification startup is automatic once this list is
// empty; there is no separate feature switch or environment mode.
func (c Config) MissingNotificationSettings() []string {
	values := []struct {
		name  string
		value string
	}{
		{name: "DINGTALK_CLIENT_ID", value: c.DingTalkClientID},
		{name: "DINGTALK_CLIENT_SECRET", value: c.DingTalkClientSecret},
		{name: "DINGTALK_ROBOT_CODE", value: c.DingTalkRobotCode},
	}
	missing := make([]string, 0, len(values))
	for _, item := range values {
		value := strings.TrimSpace(item.value)
		if value == "" || strings.Contains(value, "CHANGE_ME") {
			missing = append(missing, item.name)
		}
	}
	return missing
}

func (c Config) Validate() error {
	if missing := c.MissingNotificationSettings(); len(missing) > 0 {
		return fmt.Errorf("DingTalk notifications require %s", strings.Join(missing, ", "))
	}
	return nil
}

var ErrDisabled = errors.New("DingTalk notifications are disabled")
