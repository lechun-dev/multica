package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DingTalkOAuthProvider implements the standard DingTalk OAuth2 code flow.
// AuthURL and UserURL are overridable for staging and contract tests.
type DingTalkOAuthProvider struct {
	Client       HTTPDoer
	AuthURL      string
	TokenURL     string
	UserURL      string
	ClientID     string
	ClientSecret string
}

func (p DingTalkOAuthProvider) AuthorizationURL(_ context.Context, state, redirectURI string) (string, error) {
	if state == "" || redirectURI == "" || p.ClientID == "" {
		return "", errors.New("DingTalk OAuth state, redirect URI and client id are required")
	}
	base := p.AuthURL
	if base == "" {
		base = "https://login.dingtalk.com/oauth2/auth"
	}
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", p.ClientID)
	values.Set("scope", "openid")
	values.Set("state", state)
	values.Set("redirect_uri", redirectURI)
	return base + "?" + values.Encode(), nil
}

func (p DingTalkOAuthProvider) ExchangeCode(ctx context.Context, code, redirectURI string) (OAuthUser, error) {
	if code == "" || redirectURI == "" || p.ClientID == "" || p.ClientSecret == "" {
		return OAuthUser{}, errors.New("DingTalk OAuth code, redirect URI and client credentials are required")
	}
	tokenURL := p.TokenURL
	if tokenURL == "" {
		tokenURL = dingtalkAPIBase + "/v1.0/oauth2/userAccessToken"
	}
	payload, _ := json.Marshal(map[string]string{"clientId": p.ClientID, "clientSecret": p.ClientSecret, "code": code, "grantType": "authorization_code"})
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if err := p.postJSON(ctx, tokenURL, payload, &token); err != nil {
		return OAuthUser{}, err
	}
	if token.AccessToken == "" {
		return OAuthUser{}, errors.New("DingTalk OAuth response missing accessToken")
	}
	userURL := p.UserURL
	if userURL == "" {
		userURL = dingtalkAPIBase + "/v1.0/contact/users/me"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	if err != nil {
		return OAuthUser{}, err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token.AccessToken)
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return OAuthUser{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthUser{}, &DingTalkHTTPError{Path: userURL, Status: resp.StatusCode, Body: string(body)}
	}
	var user struct {
		DingUserID string `json:"userid"`
		UserID     string `json:"userId"`
		UnionID    string `json:"unionId"`
		OpenID     string `json:"openId"`
		Name       string `json:"nick"`
		Data       *struct {
			DingUserID string `json:"userid"`
			UserID     string `json:"userId"`
			UnionID    string `json:"unionId"`
			OpenID     string `json:"openId"`
			Name       string `json:"nick"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return OAuthUser{}, err
	}
	if user.Data != nil {
		if user.DingUserID == "" {
			user.DingUserID = user.Data.DingUserID
		}
		if user.UserID == "" {
			user.UserID = user.Data.UserID
		}
		if user.UnionID == "" {
			user.UnionID = user.Data.UnionID
		}
		if user.OpenID == "" {
			user.OpenID = user.Data.OpenID
		}
		if user.Name == "" {
			user.Name = user.Data.Name
		}
	}
	if user.DingUserID == "" {
		user.DingUserID = user.UserID
	}
	if user.DingUserID == "" && user.UnionID == "" && user.OpenID == "" {
		return OAuthUser{}, errors.New("DingTalk OAuth user response has no stable identity")
	}
	return OAuthUser{DingUserID: user.DingUserID, UnionID: user.UnionID, OpenID: user.OpenID, Name: user.Name}, nil
}

func (p DingTalkOAuthProvider) postJSON(ctx context.Context, endpoint string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &DingTalkHTTPError{Path: endpoint, Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode DingTalk OAuth response: %w", err)
	}
	return nil
}
