package dingtalkpersonal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type toolResult struct {
	Content           json.RawMessage `json:"content"`
	StructuredContent map[string]any  `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

func refreshOAuthToken(ctx context.Context, client *http.Client, endpoint, clientID, clientSecret, refreshToken string) (OAuthCredential, error) {
	payload := map[string]string{
		"clientId": clientID, "clientSecret": clientSecret,
		"refreshToken": refreshToken, "grantType": "refresh_token",
	}
	data, err := postJSON(ctx, client, endpoint, payload, "")
	if err != nil {
		var httpErr *httpCallError
		if errors.As(err, &httpErr) && (httpErr.Status == http.StatusBadRequest || httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden) {
			return OAuthCredential{}, &DeliveryError{Code: "refresh_rejected", Message: "钉钉授权已失效，请重新授权。", AuthRequired: true, Cause: err}
		}
		return OAuthCredential{}, &DeliveryError{Code: "refresh_temporarily_failed", Message: "暂时无法刷新钉钉授权，将自动重试。", Retryable: true, Cause: err}
	}
	var response struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		CorpID       string `json:"corpId"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.AccessToken == "" {
		return OAuthCredential{}, &DeliveryError{Code: "refresh_invalid_response", Message: "钉钉返回了无效的授权刷新结果，请重新授权。", AuthRequired: true, Cause: err}
	}
	if response.ExpiresIn <= 0 {
		response.ExpiresIn = 7200
	}
	return OAuthCredential{
		AccessToken: response.AccessToken, RefreshToken: response.RefreshToken,
		AccessExpiresAt:  time.Now().Add(time.Duration(response.ExpiresIn) * time.Second),
		RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour), CorpID: response.CorpID,
	}, nil
}

func (s *Service) resolveRecipient(ctx context.Context, token, userID string) (string, error) {
	data, err := s.callTool(ctx, s.contactEndpoint, token, "search_contact_by_key_word", map[string]any{"keyword": userID})
	if err != nil {
		return "", err
	}
	rows := mapSlice(data["result"])
	openIDs := make(map[string]struct{})
	exactRows := 0
	for _, row := range rows {
		if firstString(row, "userId", "userID") != userID {
			continue
		}
		exactRows++
		if openID := firstString(row, "openDingTalkId", "openDingtalkId"); openID != "" {
			openIDs[openID] = struct{}{}
		}
	}
	if len(openIDs) == 1 {
		for openID := range openIDs {
			return openID, nil
		}
	}
	if exactRows > 0 {
		return "", &DeliveryError{Code: "recipient_open_id_missing", Message: "已找到接收人，但钉钉未返回可用于私信的身份。", Identity: true}
	}
	return "", &DeliveryError{Code: "recipient_identity_unresolved", Message: "无法在当前钉钉组织中唯一找到接收人。", Identity: true}
}

func (s *Service) send(ctx context.Context, token, recipientOpenID, markdown, idempotencyKey string) (string, string, error) {
	data, err := s.callTool(ctx, s.chatEndpoint, token, "send_personal_message", map[string]any{
		"receiverOpenDingTalkId": recipientOpenID,
		"msgType":                "markdown",
		"content":                encodeContent("MissionOS", markdown),
		"uuid":                   idempotencyKey,
		"clawType":               "multica",
	})
	if err != nil {
		return "", "", err
	}
	return recursiveString(data, "openTaskId", "taskId"), recursiveString(data, "openMessageId", "messageId", "msgId"), nil
}

func (s *Service) querySendStatus(ctx context.Context, token, openTaskID string) (messageID string, pending bool, err error) {
	data, err := s.callTool(ctx, s.imEndpoint, token, "query_message_send_status", map[string]any{"openTaskId": openTaskID})
	if err != nil {
		return "", false, err
	}
	status := strings.ToUpper(recursiveString(data, "status", "sendStatus"))
	messageID = recursiveString(data, "openMessageId", "messageId", "msgId")
	switch status {
	case "SUCCESS", "SUCCEEDED", "DELIVERED":
		if messageID == "" {
			return "", false, &DeliveryError{Code: "delivery_receipt_incomplete", Message: "钉钉确认投递成功但未返回消息 ID。", Retryable: true}
		}
		return messageID, false, nil
	case "FAILED", "FAIL", "REJECTED":
		return "", false, &DeliveryError{Code: "dingtalk_delivery_failed", Message: "钉钉报告个人消息投递失败。"}
	default:
		return "", true, nil
	}
}

func (s *Service) callTool(ctx context.Context, endpoint, token, tool string, arguments map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	}
	data, err := postJSON(ctx, s.httpClient, endpoint, payload, token)
	if err != nil {
		var httpErr *httpCallError
		if errors.As(err, &httpErr) {
			switch {
			case httpErr.Status == http.StatusUnauthorized:
				return nil, &DeliveryError{Code: "authorization_expired", Message: "钉钉授权已失效，请重新授权。", AuthRequired: true, Cause: err}
			case httpErr.Status == http.StatusForbidden:
				return nil, &DeliveryError{Code: "permission_denied", Message: "当前钉钉应用未获得个人消息权限，请管理员检查应用授权。", Cause: err}
			case httpErr.Status == http.StatusRequestTimeout || httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500:
				return nil, &DeliveryError{Code: "dingtalk_temporarily_unavailable", Message: "钉钉服务暂时不可用，将自动重试。", Retryable: true, Cause: err}
			case httpErr.Status == http.StatusBadRequest:
				return nil, &DeliveryError{Code: "dws_request_invalid", Message: "钉钉个人消息接口拒绝了请求参数，请检查服务端集成版本。", Cause: err}
			case httpErr.Status == http.StatusNotFound:
				return nil, &DeliveryError{Code: "dws_endpoint_not_found", Message: "服务器配置的钉钉个人消息接口不存在，请管理员检查部署配置。", Cause: err}
			case httpErr.Status >= 400:
				return nil, &DeliveryError{Code: "dingtalk_request_rejected", Message: "钉钉拒绝了个人消息请求：" + safeRemoteMessage(httpErr.Body), Cause: err}
			}
		}
		return nil, &DeliveryError{Code: "dingtalk_request_failed", Message: "无法连接钉钉服务，将自动重试。", Retryable: true, Cause: err}
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, &DeliveryError{Code: "invalid_mcp_response", Message: "钉钉服务返回了无法识别的响应。", Retryable: true, Cause: err}
	}
	if envelope.Error != nil {
		message := strings.TrimSpace(envelope.Error.Message)
		if classified := classifyRemoteError(message); classified != nil {
			return nil, classified
		}
		if envelope.Error.Code == -32602 {
			return nil, &DeliveryError{Code: "dws_contract_mismatch", Message: "钉钉消息接口参数不兼容，请升级服务器集成。"}
		}
		return nil, &DeliveryError{Code: "dingtalk_api_error", Message: "钉钉拒绝了个人消息请求：" + safeRemoteMessage(message), Retryable: envelope.Error.Code == -32603}
	}
	var result toolResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, &DeliveryError{Code: "invalid_tool_response", Message: "钉钉消息工具返回了无法识别的响应。", Retryable: true, Cause: err}
	}
	content := result.StructuredContent
	if len(content) == 0 {
		content = decodeToolContent(result.Content)
	}
	if result.IsError {
		message := safeRemoteMessage(recursiveString(content, "message", "error", "errorMessage"))
		if classified := classifyRemoteError(message); classified != nil {
			return nil, classified
		}
		return nil, &DeliveryError{Code: "dingtalk_tool_error", Message: "钉钉未接受个人消息：" + message}
	}
	return content, nil
}

func classifyRemoteError(message string) *DeliveryError {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(lower, "unauthor"),
		strings.Contains(lower, "not login"),
		strings.Contains(lower, "未登录"),
		strings.Contains(lower, "token") && (strings.Contains(lower, "expire") || strings.Contains(lower, "invalid")):
		return &DeliveryError{Code: "authorization_expired", Message: "钉钉授权已失效，请重新授权。", AuthRequired: true}
	case strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "no permission"),
		strings.Contains(lower, "无权限"),
		strings.Contains(lower, "权限不足"):
		return &DeliveryError{Code: "permission_denied", Message: "当前钉钉应用未获得个人消息权限，请管理员检查应用授权。"}
	case strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "too many request"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "temporarily unavailable"),
		strings.Contains(lower, "限流"),
		strings.Contains(lower, "超时"):
		return &DeliveryError{Code: "dingtalk_temporarily_unavailable", Message: "钉钉服务暂时不可用，将自动重试。", Retryable: true}
	default:
		return nil
	}
}

func decodeToolContent(raw json.RawMessage) map[string]any {
	var object map[string]any
	if json.Unmarshal(raw, &object) == nil {
		return object
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, block := range blocks {
			if json.Unmarshal([]byte(block.Text), &object) == nil {
				return object
			}
		}
	}
	return map[string]any{}
}

type httpCallError struct {
	Status int
	Body   string
}

func (e *httpCallError) Error() string { return "DingTalk HTTP " + strconv.Itoa(e.Status) }

func postJSON(ctx context.Context, client *http.Client, endpoint string, payload any, token string) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Cli-Source", "multica-server")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("x-user-access-token", token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &httpCallError{Status: response.StatusCode, Body: safeRemoteMessage(string(data))}
	}
	return data, nil
}

func safeRemoteMessage(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "未知原因"
	}
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func mapSlice(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func recursiveString(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		if found := firstString(typed, keys...); found != "" {
			return found
		}
		for _, child := range typed {
			if found := recursiveString(child, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := recursiveString(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}
