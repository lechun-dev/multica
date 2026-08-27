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
	return fmt.Sprintf("DingTalk %s returned HTTP %d", e.Path, e.Status)
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
	body := map[string]any{"robotCode": robotCode, "msgKey": "sampleMarkdown", "msgParam": mustMarkdownParam(message.Text)}
	path := "/v1.0/robot/oToMessages/batchSend"
	if message.ChannelType == "p2p" {
		body["userIds"] = []string{message.DingUserID}
	} else {
		path = "/v1.0/robot/groupMessages/send"
		body["openConversationId"] = message.ChannelID
	}
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
	b, _ := json.Marshal(map[string]string{"title": "Multica 通知", "text": text})
	return string(b)
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
		var envelope struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(bodyBytes, &envelope)
		return &DingTalkHTTPError{Path: path, Status: resp.StatusCode, Code: envelope.Code, Body: string(bodyBytes)}
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &DingTalkHTTPError{Path: "/v1.0/oauth2/accessToken", Status: resp.StatusCode, Body: string(body)}
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
