package notify

import (
	"os"
	"strconv"
)

// ConfigFromEnv reads module configuration without requiring an infrastructure
// connection. This makes local/mock usable before a server or database exists.
func ConfigFromEnv(getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Config{
		Mode:                 getenv("DINGTALK_NOTIFY_MODE"),
		Enabled:              envBool(getenv("DINGTALK_NOTIFY_ENABLED")),
		AppBaseURL:           getenv("APP_BASE_URL"),
		APIBaseURL:           getenv("API_PUBLIC_URL"),
		DatabaseURL:          getenv("DATABASE_URL"),
		RedisURL:             getenv("REDIS_URL"),
		EncryptionKey:        getenv("ENCRYPTION_KEY"),
		SecretStoreRef:       getenv("SECRET_STORE_REF"),
		DingTalkClientID:     getenv("DINGTALK_CLIENT_ID"),
		DingTalkClientSecret: getenv("DINGTALK_CLIENT_SECRET"),
		DingTalkCorpID:       getenv("DINGTALK_CORP_ID"),
		DingTalkRedirectURI:  getenv("DINGTALK_OAUTH_REDIRECT_URI"),
		DingTalkAgentID:      getenv("DINGTALK_AGENT_ID"),
		DingTalkRobotCode:    getenv("DINGTALK_ROBOT_CODE"),
	}
}

func envBool(value string) bool {
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
