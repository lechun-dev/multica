package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	dingtalkPersonalMessagePollInterval = 30 * time.Second
	dingtalkPersonalMessageBatchLimit   = 20
	dingtalkPersonalMessageQueryTimeout = 30 * time.Second
)

type DingTalkPersonalMessage struct {
	ID                  string `json:"id"`
	WorkspaceID         string `json:"workspace_id"`
	SenderDingUserID    string `json:"sender_ding_user_id"`
	SenderUnionID       string `json:"sender_union_id,omitempty"`
	SenderCorpID        string `json:"sender_corp_id,omitempty"`
	RecipientDingUserID string `json:"recipient_ding_user_id"`
	Markdown            string `json:"markdown"`
	IdempotencyKey      string `json:"idempotency_key"`
	DWSOpenTaskID       string `json:"dws_open_task_id,omitempty"`
	ExpiresAt           string `json:"expires_at"`
}

type DingTalkPersonalMessageResult struct {
	DaemonID         string `json:"daemon_id"`
	Status           string `json:"status"`
	DWSOpenTaskID    string `json:"dws_open_task_id,omitempty"`
	DWSOpenMessageID string `json:"dws_open_message_id,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
}

type DingTalkPersonalMessageHealth struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

func (c *Client) ClaimDingTalkPersonalMessage(ctx context.Context, daemonID string) (*DingTalkPersonalMessage, error) {
	var response struct {
		Message *DingTalkPersonalMessage `json:"message"`
	}
	if err := c.postJSON(ctx, "/api/daemon/dingtalk-personal-messages/claim", map[string]string{
		"daemon_id": daemonID,
	}, &response); err != nil {
		return nil, err
	}
	return response.Message, nil
}

func (c *Client) ReportDingTalkPersonalMessageResult(ctx context.Context, id string, result DingTalkPersonalMessageResult) error {
	var response struct {
		OK bool `json:"ok"`
	}
	return c.postJSON(ctx, "/api/daemon/dingtalk-personal-messages/"+id+"/result", result, &response)
}

func (d *Daemon) dingtalkPersonalMessageLoop(ctx context.Context) {
	d.drainDingTalkPersonalMessages(ctx)
	ticker := time.NewTicker(dingtalkPersonalMessagePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.drainDingTalkPersonalMessages(ctx)
		}
	}
}

func (d *Daemon) drainDingTalkPersonalMessages(ctx context.Context) {
	if d == nil || d.client == nil {
		return
	}
	d.dingtalkPersonalMu.Lock()
	if d.dingtalkPersonalInflight {
		d.dingtalkPersonalMu.Unlock()
		return
	}
	d.dingtalkPersonalInflight = true
	d.dingtalkPersonalMu.Unlock()
	defer func() {
		d.dingtalkPersonalMu.Lock()
		d.dingtalkPersonalInflight = false
		d.dingtalkPersonalMu.Unlock()
	}()

	for i := 0; i < dingtalkPersonalMessageBatchLimit && ctx.Err() == nil; i++ {
		message, err := d.client.ClaimDingTalkPersonalMessage(ctx, d.cfg.DaemonID)
		if err != nil {
			var requestErr *requestError
			if errors.As(err, &requestErr) && (requestErr.StatusCode == 404 || requestErr.StatusCode == 403 || requestErr.StatusCode == 426) {
				d.logger.Debug("DingTalk personal messages unavailable on this server", "status", requestErr.StatusCode)
				return
			}
			d.logger.Debug("DingTalk personal-message claim failed", "error", err)
			return
		}
		if message == nil {
			return
		}
		d.deliverDingTalkPersonalMessage(ctx, message)
	}
}

func (d *Daemon) deliverDingTalkPersonalMessage(ctx context.Context, message *DingTalkPersonalMessage) {
	result := d.runDWSMessageDelivery(ctx, message)
	result.DaemonID = d.cfg.DaemonID
	if err := d.client.ReportDingTalkPersonalMessageResult(ctx, message.ID, result); err != nil {
		d.logger.Warn("DingTalk personal-message result report failed", "message_id", message.ID, "error", err)
		return
	}
	switch result.Status {
	case "waiting_for_dws_login", "waiting_for_identity", "failed":
		d.setDingTalkPersonalMessageHealth(result.ErrorCode, result.ErrorMessage)
	case "delivered":
		d.setDingTalkPersonalMessageHealth("ready", "")
	}
}

func (d *Daemon) setDingTalkPersonalMessageHealth(state, message string) {
	d.dingtalkPersonalMu.Lock()
	d.dingtalkPersonalHealth = DingTalkPersonalMessageHealth{State: state, Message: message}
	d.dingtalkPersonalMu.Unlock()
}

func (d *Daemon) dingtalkPersonalMessageHealthSnapshot() *DingTalkPersonalMessageHealth {
	d.dingtalkPersonalMu.Lock()
	defer d.dingtalkPersonalMu.Unlock()
	if d.dingtalkPersonalHealth.State == "" {
		return nil
	}
	copy := d.dingtalkPersonalHealth
	return &copy
}

func (d *Daemon) runDWSMessageDelivery(ctx context.Context, message *DingTalkPersonalMessage) DingTalkPersonalMessageResult {
	path, err := dwsExecutablePath()
	if err != nil {
		return dwsWaitingResult("waiting_for_dws_login", "dws_not_installed", "未找到 dws CLI。请先安装 DWS，并执行 dws auth login。", err)
	}
	authOutput, err := runDWSCommand(ctx, path, "auth", "status", "--format", "json")
	if err != nil || !jsonBoolean(authOutput, "authenticated") {
		return dwsWaitingResult("waiting_for_dws_login", "dws_not_logged_in", "DWS 尚未登录。请在终端执行 dws auth login 后重试。", err)
	}
	selfOutput, err := runDWSCommand(ctx, path, "contact", "user", "get-self", "--format", "json")
	if err != nil {
		if dwsLooksUnauthenticated(err.Error()) {
			return dwsWaitingResult("waiting_for_dws_login", "dws_not_logged_in", "DWS 登录已失效。请在终端执行 dws auth login。", err)
		}
		return dwsWaitingResult("retry", "dws_identity_check_failed", "无法核对当前 DWS 身份，将自动重试。", err)
	}
	actualUnionID := jsonStringRecursive(selfOutput, "unionId", "union_id")
	actualUserID := jsonStringRecursive(selfOutput, "userId", "userid", "user_id")
	actualCorpID := jsonStringRecursive(selfOutput, "corpId", "corp_id")
	if !dwsIdentityMatches(message, actualUnionID, actualUserID, actualCorpID) {
		return DingTalkPersonalMessageResult{
			Status:       "waiting_for_identity",
			ErrorCode:    "dws_identity_mismatch",
			ErrorMessage: "当前 DWS 账号与 Multica 登录的钉钉账号不一致。请切换账号并重新执行 dws auth login。",
		}
	}

	openTaskID := strings.TrimSpace(message.DWSOpenTaskID)
	if openTaskID == "" {
		sendOutput, sendErr := runDWSCommand(ctx, path,
			"chat", "message", "send",
			"--user", message.RecipientDingUserID,
			"--title", "MissionOS 通知",
			"--content", message.Markdown,
			"--idempotency-key", message.IdempotencyKey,
			"--ai-tag=false",
			"--format", "json",
		)
		if sendErr != nil {
			if dwsLooksUnauthenticated(sendErr.Error()) {
				return dwsWaitingResult("waiting_for_dws_login", "dws_not_logged_in", "DWS 登录已失效。请在终端执行 dws auth login。", sendErr)
			}
			return dwsWaitingResult("retry", "dws_send_failed", "DWS 私信发送失败，将自动重试。", sendErr)
		}
		openTaskID = jsonStringRecursive(sendOutput, "openTaskId", "open_task_id")
		if openTaskID == "" {
			return dwsWaitingResult("retry", "dws_missing_open_task_id", "DWS 未返回发送任务 ID，将自动重试。", nil)
		}
		if err := d.client.ReportDingTalkPersonalMessageResult(ctx, message.ID, DingTalkPersonalMessageResult{
			DaemonID:      d.cfg.DaemonID,
			Status:        "submitted",
			DWSOpenTaskID: openTaskID,
		}); err != nil {
			return DingTalkPersonalMessageResult{Status: "retry", DWSOpenTaskID: openTaskID, ErrorCode: "submit_checkpoint_failed", ErrorMessage: "发送已提交，但无法保存 DWS 任务 ID；将使用幂等键重试。"}
		}
	}

	queryCtx, cancel := context.WithTimeout(ctx, dingtalkPersonalMessageQueryTimeout)
	defer cancel()
	for queryCtx.Err() == nil {
		queryOutput, queryErr := runDWSCommand(queryCtx, path,
			"chat", "message", "query-send-status",
			"--open-task-id", openTaskID,
			"--format", "json",
		)
		if queryErr != nil {
			if dwsLooksUnauthenticated(queryErr.Error()) {
				return DingTalkPersonalMessageResult{Status: "waiting_for_dws_login", DWSOpenTaskID: openTaskID, ErrorCode: "dws_not_logged_in", ErrorMessage: "DWS 登录已失效。请在终端执行 dws auth login。"}
			}
			return DingTalkPersonalMessageResult{Status: "retry", DWSOpenTaskID: openTaskID, ErrorCode: "dws_status_query_failed", ErrorMessage: compactDWSError(queryErr)}
		}
		openMessageID := jsonStringRecursive(queryOutput, "openMessageId", "open_message_id")
		if openMessageID != "" {
			return DingTalkPersonalMessageResult{Status: "delivered", DWSOpenTaskID: openTaskID, DWSOpenMessageID: openMessageID}
		}
		status := strings.ToLower(jsonStringRecursive(queryOutput, "status", "sendStatus", "send_status"))
		if strings.Contains(status, "fail") || strings.Contains(status, "error") || strings.Contains(status, "cancel") {
			return DingTalkPersonalMessageResult{Status: "retry", DWSOpenTaskID: openTaskID, ErrorCode: "dws_delivery_failed", ErrorMessage: "DWS 报告消息投递失败，将自动重试。"}
		}
		select {
		case <-queryCtx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return DingTalkPersonalMessageResult{Status: "retry", DWSOpenTaskID: openTaskID, ErrorCode: "dws_delivery_pending", ErrorMessage: "DWS 消息仍在投递，将自动继续查询。"}
}

func dwsExecutablePath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MULTICA_DWS_PATH")); configured != "" {
		info, err := os.Stat(configured)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("configured DWS executable is unavailable")
		}
		return configured, nil
	}
	return exec.LookPath("dws")
}

func runDWSCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("dws command failed: %s", compactDWSOutput(output))
	}
	if apiError := dwsJSONError(output); apiError != "" {
		return output, fmt.Errorf("dws command failed: %s", apiError)
	}
	return output, nil
}

func dwsIdentityMatches(message *DingTalkPersonalMessage, unionID, userID, corpID string) bool {
	if message == nil {
		return false
	}
	if expectedUnionID := strings.TrimSpace(message.SenderUnionID); expectedUnionID != "" {
		return unionID != "" && unionID == expectedUnionID
	}
	expectedUserID := strings.TrimSpace(message.SenderDingUserID)
	expectedCorpID := strings.TrimSpace(message.SenderCorpID)
	if expectedUserID == "" || expectedCorpID == "" {
		return false
	}
	return userID == expectedUserID && corpID == expectedCorpID
}

func dwsJSONError(data []byte) string {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	failed := false
	if success, ok := value["success"].(bool); ok && !success {
		failed = true
	}
	if errorValue, ok := value["error"]; ok && errorValue != nil {
		switch typed := errorValue.(type) {
		case string:
			failed = failed || strings.TrimSpace(typed) != ""
		case map[string]any:
			failed = failed || len(typed) != 0
		default:
			failed = true
		}
	}
	if !failed {
		return ""
	}
	message := jsonStringRecursive(data, "message", "errorMessage", "error_message", "msg")
	code := jsonStringRecursive(data, "code", "errorCode", "error_code")
	if message == "" {
		message = compactDWSOutput(data)
	}
	if code != "" && !strings.Contains(message, code) {
		message = code + ": " + message
	}
	return strings.TrimSpace(message)
}

func jsonBoolean(data []byte, key string) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	return findJSONBoolean(value, key)
}

func findJSONBoolean(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := typed[key].(bool); ok {
			return found
		}
		for _, child := range typed {
			if findJSONBoolean(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if findJSONBoolean(child, key) {
				return true
			}
		}
	}
	return false
}

func jsonStringRecursive(data []byte, keys ...string) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	return findJSONString(value, wanted)
}

func findJSONString(value any, keys map[string]struct{}) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, ok := keys[key]; ok {
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
		for _, child := range typed {
			if found := findJSONString(child, keys); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findJSONString(child, keys); found != "" {
				return found
			}
		}
	}
	return ""
}

func dwsWaitingResult(status, code, message string, err error) DingTalkPersonalMessageResult {
	if err != nil {
		message += " " + compactDWSError(err)
	}
	return DingTalkPersonalMessageResult{Status: status, ErrorCode: code, ErrorMessage: strings.TrimSpace(message)}
}

func compactDWSError(err error) string {
	if err == nil {
		return ""
	}
	return compactDWSOutput([]byte(err.Error()))
}

func compactDWSOutput(output []byte) string {
	message := strings.TrimSpace(string(output))
	for _, marker := range []string{"access_token", "refresh_token", "client_secret", "clientSecret"} {
		if strings.Contains(strings.ToLower(message), strings.ToLower(marker)) {
			return "DWS returned a credential-related error; details were redacted"
		}
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func dwsLooksUnauthenticated(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "未登录") || strings.Contains(lower, "not logged") ||
		strings.Contains(lower, "auth_token_expired") || strings.Contains(lower, "user_token_illegal") ||
		strings.Contains(lower, "token验证失败")
}
