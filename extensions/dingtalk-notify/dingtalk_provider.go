package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const dingtalkAPIBase = "https://api.dingtalk.com"

const (
	msgKeyMarkdown   = "sampleMarkdown"
	msgKeyActionCard = "sampleActionCard"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type DingTalkHTTPError struct {
	Path    string
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *DingTalkHTTPError) Error() string {
	if e.Message != "" {
		if e.Code != "" {
			return fmt.Sprintf("DingTalk %s returned HTTP %d (%s): %s", e.Path, e.Status, e.Code, e.Message)
		}
		return fmt.Sprintf("DingTalk %s returned HTTP %d: %s", e.Path, e.Status, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("DingTalk %s returned HTTP %d (%s)", e.Path, e.Status, e.Code)
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		if len(body) > 512 {
			body = body[:512] + "..."
		}
		return fmt.Sprintf("DingTalk %s returned HTTP %d: %s", e.Path, e.Status, body)
	}
	return fmt.Sprintf("DingTalk %s returned HTTP %d", e.Path, e.Status)
}

// newDingTalkHTTPError converts both the v1.0 {code,message} envelope and the
// legacy {errcode,errmsg} envelope into one structured error. A nil result
// means the response is successful and does not contain an API error envelope.
func newDingTalkHTTPError(path string, status int, body []byte) *DingTalkHTTPError {
	var envelope struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		ErrorCode    string `json:"errorCode"`
		ErrorMessage string `json:"errorMessage"`
		ErrCode      int    `json:"errcode"`
		ErrMsg       string `json:"errmsg"`
	}
	_ = json.Unmarshal(body, &envelope)
	code := strings.TrimSpace(envelope.Code)
	if code == "" {
		code = strings.TrimSpace(envelope.ErrorCode)
	}
	message := strings.TrimSpace(envelope.Message)
	if message == "" {
		message = strings.TrimSpace(envelope.ErrorMessage)
	}
	if envelope.ErrCode != 0 {
		if code == "" {
			code = fmt.Sprintf("errcode:%d", envelope.ErrCode)
		}
		if message == "" {
			message = strings.TrimSpace(envelope.ErrMsg)
		}
	}
	if status >= 200 && status < 300 && code == "" && envelope.ErrCode == 0 {
		return nil
	}
	return &DingTalkHTTPError{
		Path:    path,
		Status:  status,
		Code:    code,
		Message: message,
		Body:    strings.TrimSpace(string(body)),
	}
}

func transientDingTalkError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *DingTalkHTTPError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500) {
		return RetryableError{Err: err}
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return RetryableError{Err: err}
	}
	return err
}

type DingTalkProvider struct {
	Client       HTTPDoer
	BaseURL      string
	ClientID     string
	ClientSecret string
	RobotCode    string
	Now          func() time.Time

	mu          sync.Mutex
	accessToken string
	tokenUntil  time.Time
}

func (p *DingTalkProvider) Send(ctx context.Context, message Message) error {
	if message.ChannelType != "p2p" && message.ChannelType != "group" {
		return errors.New("DingTalk message channel type must be p2p or group")
	}
	robotCode := message.RobotCode
	if robotCode == "" {
		robotCode = p.RobotCode
	}
	if robotCode == "" || p.ClientID == "" || p.ClientSecret == "" {
		return errors.New("DingTalk provider credentials and robot code are required")
	}
	if message.ChannelType == "p2p" && message.DingUserID == "" {
		return errors.New("DingTalk p2p message recipient is required")
	}
	if message.ChannelType == "group" && message.ChannelID == "" {
		return errors.New("DingTalk group message channel is required")
	}
	msgKey := msgKeyMarkdown
	msgParam := mustMarkdownParam(message.Text)
	if cardParam, ok := mustActionCardParam(message.Text); ok {
		msgKey = msgKeyActionCard
		msgParam = cardParam
	}
	body := map[string]any{"robotCode": robotCode, "msgKey": msgKey, "msgParam": msgParam}
	path := "/v1.0/robot/oToMessages/batchSend"
	if message.ChannelType == "p2p" {
		body["userIds"] = []string{message.DingUserID}
	} else {
		path = "/v1.0/robot/groupMessages/send"
		body["openConversationId"] = message.ChannelID
	}
	if err := p.sendWithRefresh(ctx, path, body); err != nil {
		// Action cards are supported by the current DingTalk robot API, but some
		// older robot installations only accept sampleMarkdown. Fall back only
		// for an explicit unsupported-message-type response so transient failures
		// are still retried by the outbox worker instead of sending duplicates.
		if msgKey == msgKeyActionCard && isUnsupportedActionCardError(err) {
			body["msgKey"] = msgKeyMarkdown
			body["msgParam"] = mustMarkdownParam(message.Text)
			return p.sendWithRefresh(ctx, path, body)
		}
		return err
	}
	return nil
}

func (p *DingTalkProvider) sendWithRefresh(ctx context.Context, path string, body map[string]any) error {
	if err := p.sendJSON(ctx, path, body, false); err != nil {
		var httpErr *DingTalkHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized {
			p.invalidateToken()
			return transientDingTalkError(p.sendJSON(ctx, path, body, true))
		}
		return transientDingTalkError(err)
	}
	return nil
}

func mustMarkdownParam(text string) string {
	b, _ := json.Marshal(map[string]string{"title": markdownMessageTitle(text), "text": text})
	return string(b)
}

// mustActionCardParam converts the notification format into DingTalk's
// single-button ActionCard payload. The persisted notification text remains
// the source of truth, so queued messages created before this feature can also
// be sent as cards without an outbox schema migration.
func mustActionCardParam(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	first, last := -1, -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first == -1 {
			first = i
		}
		last = i
	}
	if first == -1 {
		return "", false
	}

	// Keep the human-readable bell title in the card body. Some DingTalk
	// clients hide the ActionCard `title` field, so relying on it alone loses
	// the notification context on mobile.
	const cardTitle = "MissionOS 通知"
	textLines := append([]string(nil), lines[first:last+1]...)
	if len(textLines) == 0 {
		return "", false
	}
	lastLine := strings.TrimSpace(textLines[len(textLines)-1])
	const buttonPrefix = "[打开任务并回复]("
	lastLine = strings.TrimPrefix(lastLine, "**")
	lastLine = strings.TrimSuffix(lastLine, "**")
	if !strings.HasPrefix(lastLine, buttonPrefix) || !strings.HasSuffix(lastLine, ")") {
		return "", false
	}
	actionURL := strings.TrimSuffix(strings.TrimPrefix(lastLine, buttonPrefix), ")")
	if actionURL == "" {
		return "", false
	}
	textLines = textLines[:len(textLines)-1]
	cardText := strings.TrimSpace(strings.Join(textLines, "\n"))
	if cardText == "" {
		return "", false
	}
	b, err := json.Marshal(map[string]string{
		"title":       cardTitle,
		"text":        cardText,
		"singleTitle": "打开任务并回复",
		"singleURL":   actionURL,
	})
	if err != nil {
		return "", false
	}
	return string(b), true
}

func isUnsupportedActionCardError(err error) bool {
	var apiErr *DingTalkHTTPError
	if !errors.As(err, &apiErr) || apiErr.Status < 400 || apiErr.Status >= 500 {
		return false
	}
	detail := strings.ToLower(strings.Join([]string{apiErr.Code, apiErr.Message, apiErr.Body}, " "))
	if !strings.Contains(detail, "actioncard") &&
		!strings.Contains(detail, "msgkey") &&
		!strings.Contains(detail, "unsupported") &&
		!strings.Contains(detail, "not support") &&
		!strings.Contains(detail, "不支持") &&
		!strings.Contains(detail, "消息类型") {
		return false
	}
	return strings.Contains(detail, "unsupported") ||
		strings.Contains(detail, "not support") ||
		strings.Contains(detail, "not_support") ||
		strings.Contains(detail, "invalid") ||
		strings.Contains(detail, "不支持") ||
		strings.Contains(detail, "消息类型")
}

// markdownMessageTitle is used by DingTalk's push preview. Derive it from the
// first line of the persisted body so queued notifications remain descriptive
// after a worker restart, without adding another outbox column.
func markdownMessageTitle(text string) string {
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		line = strings.TrimSpace(candidate)
		if line != "" {
			break
		}
	}
	line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
	line = strings.TrimSpace(line)
	if line == "" {
		return "MissionOS 通知"
	}
	if runes := []rune(line); len(runes) > 64 {
		return string(runes[:64]) + "…"
	}
	return line
}

func (p *DingTalkProvider) sendJSON(ctx context.Context, path string, payload any, forceRefresh bool) error {
	token, err := p.token(ctx, forceRefresh)
	if err != nil {
		return err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = dingtalkAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newDingTalkHTTPError(path, resp.StatusCode, bodyBytes)
	}
	return nil
}

func (p *DingTalkProvider) token(ctx context.Context, forceRefresh bool) (string, error) {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	p.mu.Lock()
	if !forceRefresh && p.accessToken != "" && p.tokenUntil.After(now.Add(30*time.Second)) {
		tok := p.accessToken
		p.mu.Unlock()
		return tok, nil
	}
	p.mu.Unlock()
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = dingtalkAPIBase
	}
	payload, _ := json.Marshal(map[string]string{"appKey": p.ClientID, "appSecret": p.ClientSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1.0/oauth2/accessToken", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if apiErr := newDingTalkHTTPError("/v1.0/oauth2/accessToken", resp.StatusCode, body); apiErr != nil {
		return "", apiErr
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("DingTalk token response missing accessToken")
	}
	if out.ExpireIn <= 0 {
		out.ExpireIn = 7200
	}
	p.mu.Lock()
	p.accessToken, p.tokenUntil = out.AccessToken, now.Add(time.Duration(out.ExpireIn)*time.Second)
	p.mu.Unlock()
	return out.AccessToken, nil
}

func (p *DingTalkProvider) invalidateToken() {
	p.mu.Lock()
	p.accessToken, p.tokenUntil = "", time.Time{}
	p.mu.Unlock()
}
