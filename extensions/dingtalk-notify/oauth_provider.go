package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DingTalkOAuthProvider implements the standard DingTalk OAuth2 code flow.
// AuthURL and UserURL are overridable for staging and contract tests.
type DingTalkOAuthProvider struct {
	Client              HTTPDoer
	AuthURL             string
	TokenURL            string
	UserURL             string
	AppTokenURL         string
	UnionLookupURL      string
	UserDetailURL       string
	DepartmentDetailURL string
	ClientID            string
	ClientSecret        string
	Scope               string
	CorpID              string
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
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		scope = "openid"
		if strings.TrimSpace(p.CorpID) != "" {
			scope = "openid corpid"
		}
	}
	values.Set("scope", scope)
	if corpID := strings.TrimSpace(p.CorpID); corpID != "" {
		values.Set("corpId", corpID)
	}
	// Force a fresh grant so newly enabled DingTalk permissions are reflected
	// in the user access token instead of reusing a stale authorization.
	values.Set("prompt", "consent")
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
		return OAuthUser{}, newDingTalkHTTPError(userURL, resp.StatusCode, body)
	}
	var user struct {
		DingUserID string `json:"userid"`
		UserID     string `json:"userId"`
		UnionID    string `json:"unionId"`
		OpenID     string `json:"openId"`
		Name       string `json:"nick"`
		Email      string `json:"email"`
		AvatarURL  string `json:"avatarUrl"`
		Avatar     string `json:"avatar"`
		Data       *struct {
			DingUserID string `json:"userid"`
			UserID     string `json:"userId"`
			UnionID    string `json:"unionId"`
			OpenID     string `json:"openId"`
			Name       string `json:"nick"`
			Email      string `json:"email"`
			AvatarURL  string `json:"avatarUrl"`
			Avatar     string `json:"avatar"`
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
		if user.Email == "" {
			user.Email = user.Data.Email
		}
		if user.AvatarURL == "" {
			user.AvatarURL = user.Data.AvatarURL
		}
		if user.Avatar == "" {
			user.Avatar = user.Data.Avatar
		}
	}
	if user.AvatarURL == "" {
		user.AvatarURL = user.Avatar
	}
	if user.DingUserID == "" {
		user.DingUserID = user.UserID
	}
	if user.DingUserID == "" && user.UnionID == "" && user.OpenID == "" {
		return OAuthUser{}, errors.New("DingTalk OAuth user response has no stable identity")
	}
	identity := OAuthUser{DingUserID: user.DingUserID, UnionID: user.UnionID, OpenID: user.OpenID, Name: user.Name, Email: user.Email, AvatarURL: user.AvatarURL}
	needsEnterpriseIdentity := identity.DingUserID == "" || identity.Name == "" || identity.Email == "" || identity.AvatarURL == ""
	if identity.DingUserID != "" || identity.UnionID != "" {
		enterpriseIdentity, err := p.enterpriseIdentity(ctx, identity.DingUserID, identity.UnionID)
		if err != nil {
			if needsEnterpriseIdentity {
				return OAuthUser{}, err
			}
			return identity, nil
		}
		if identity.DingUserID == "" {
			identity.DingUserID = enterpriseIdentity.DingUserID
		}
		if identity.Name == "" {
			identity.Name = enterpriseIdentity.Name
		}
		if identity.Email == "" {
			identity.Email = enterpriseIdentity.Email
		}
		if identity.AvatarURL == "" {
			identity.AvatarURL = enterpriseIdentity.AvatarURL
		}
		identity.Departments = enterpriseIdentity.Departments
		identity.DepartmentsSynced = enterpriseIdentity.DepartmentsSynced
	}
	return identity, nil
}

func (p DingTalkOAuthProvider) enterpriseIdentity(ctx context.Context, dingUserID, unionID string) (OAuthUser, error) {
	appTokenURL := p.AppTokenURL
	if appTokenURL == "" {
		appTokenURL = dingtalkAPIBase + "/v1.0/oauth2/accessToken"
	}
	tokenPayload, _ := json.Marshal(map[string]string{"appKey": p.ClientID, "appSecret": p.ClientSecret})
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if err := p.postJSON(ctx, appTokenURL, tokenPayload, &token); err != nil {
		return OAuthUser{}, fmt.Errorf("load DingTalk application token: %w", err)
	}
	if token.AccessToken == "" {
		return OAuthUser{}, errors.New("DingTalk application token response missing accessToken")
	}

	// The OAuth `userId` value is not guaranteed to be the enterprise
	// directory userid (some OAuth tenants return an openId-shaped value).
	// When unionId is available, resolve it first so the subsequent user detail
	// request — and its dept_id_list — always targets the real directory user.
	if unionID != "" {
		unionURL := p.UnionLookupURL
		if unionURL == "" {
			unionURL = "https://oapi.dingtalk.com/topapi/user/getbyunionid"
		}
		unionPayload, _ := json.Marshal(map[string]string{"unionid": unionID})
		var lookup struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
			Result  struct {
				UserID string `json:"userid"`
			} `json:"result"`
		}
		if err := p.postAppJSON(ctx, unionURL, token.AccessToken, unionPayload, &lookup); err != nil {
			if dingUserID == "" {
				return OAuthUser{}, fmt.Errorf("resolve DingTalk union id: %w", err)
			}
			slog.WarnContext(ctx, "dingtalk login: union id lookup unavailable", "error", err)
		} else if lookup.ErrCode != 0 || lookup.Result.UserID == "" {
			if dingUserID == "" {
				if lookup.ErrCode != 0 {
					return OAuthUser{}, fmt.Errorf("resolve DingTalk union id: DingTalk error %d", lookup.ErrCode)
				}
				return OAuthUser{}, errors.New("DingTalk union id response missing userid")
			}
			slog.WarnContext(ctx, "dingtalk login: union id response did not include directory userid", "errcode", lookup.ErrCode)
		} else {
			dingUserID = lookup.Result.UserID
		}
	}
	if dingUserID == "" {
		return OAuthUser{}, errors.New("DingTalk enterprise user id is required")
	}

	detailURL := p.UserDetailURL
	if detailURL == "" {
		detailURL = "https://oapi.dingtalk.com/topapi/v2/user/get"
	}
	detailPayload, _ := json.Marshal(map[string]string{"userid": dingUserID, "language": "zh_CN"})
	var detail struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UserID     string        `json:"userid"`
			UnionID    string        `json:"unionid"`
			Name       string        `json:"name"`
			Email      string        `json:"email"`
			AvatarURL  string        `json:"avatarUrl"`
			Avatar     string        `json:"avatar"`
			DeptIDList *[]dingTalkID `json:"dept_id_list"`
		} `json:"result"`
	}
	if err := p.postAppJSON(ctx, detailURL, token.AccessToken, detailPayload, &detail); err != nil {
		return OAuthUser{}, fmt.Errorf("load DingTalk enterprise user: %w", err)
	}
	if detail.ErrCode != 0 {
		return OAuthUser{}, fmt.Errorf("load DingTalk enterprise user: DingTalk error %d", detail.ErrCode)
	}
	if detail.Result.UserID == "" {
		detail.Result.UserID = dingUserID
	}
	if detail.Result.AvatarURL == "" {
		detail.Result.AvatarURL = detail.Result.Avatar
	}
	slog.InfoContext(ctx, "dingtalk login: enterprise user profile loaded",
		"has_department_list", detail.Result.DeptIDList != nil,
		"department_id_count", departmentIDCount(detail.Result.DeptIDList))
	identity := OAuthUser{
		DingUserID: detail.Result.UserID,
		UnionID:    detail.Result.UnionID,
		Name:       detail.Result.Name,
		Email:      strings.ToLower(strings.TrimSpace(detail.Result.Email)),
		AvatarURL:  detail.Result.AvatarURL,
	}
	if detail.Result.DeptIDList != nil {
		departments, err := p.loadDepartments(ctx, token.AccessToken, *detail.Result.DeptIDList)
		if err != nil {
			slog.WarnContext(ctx, "dingtalk login: department profile unavailable",
				"department_id_count", len(*detail.Result.DeptIDList), "error", err)
		} else {
			identity.Departments = departments
			identity.DepartmentsSynced = true
			slog.InfoContext(ctx, "dingtalk login: department profile synchronized",
				"department_count", len(departments))
		}
	} else {
		slog.WarnContext(ctx, "dingtalk login: enterprise profile did not include department list")
	}
	return identity, nil
}

// departmentIDCount is intentionally limited to a count so diagnostics never
// emit DingTalk department identifiers.
func departmentIDCount(ids *[]dingTalkID) int {
	if ids == nil {
		return 0
	}
	return len(*ids)
}

type dingTalkID string

func (id *dingTalkID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*id = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*id = dingTalkID(value)
		return nil
	}
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*id = dingTalkID(value.String())
	return nil
}

func (p DingTalkOAuthProvider) loadDepartments(ctx context.Context, accessToken string, departmentIDs []dingTalkID) ([]DingTalkDepartment, error) {
	endpoint := p.DepartmentDetailURL
	if endpoint == "" {
		endpoint = "https://oapi.dingtalk.com/topapi/v2/department/get"
	}
	departments := make([]DingTalkDepartment, 0, len(departmentIDs))
	seen := make(map[string]struct{}, len(departmentIDs))
	for _, rawID := range departmentIDs {
		id := strings.TrimSpace(string(rawID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		var payloadID any = id
		if numericID, err := strconv.ParseInt(id, 10, 64); err == nil {
			payloadID = numericID
		}
		payload, _ := json.Marshal(map[string]any{"dept_id": payloadID, "language": "zh_CN"})
		var detail struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
			Result  struct {
				Name string `json:"name"`
			} `json:"result"`
		}
		if err := p.postAppJSON(ctx, endpoint, accessToken, payload, &detail); err != nil {
			return nil, fmt.Errorf("load DingTalk department: %w", err)
		}
		if detail.ErrCode != 0 {
			return nil, fmt.Errorf("load DingTalk department: DingTalk error %d", detail.ErrCode)
		}
		name := strings.TrimSpace(detail.Result.Name)
		if name == "" {
			return nil, errors.New("load DingTalk department: response missing name")
		}
		departments = append(departments, DingTalkDepartment{ID: id, Name: name})
	}
	return departments, nil
}

func (p DingTalkOAuthProvider) postAppJSON(ctx context.Context, endpoint, accessToken string, payload []byte, out any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	query := parsed.Query()
	query.Set("access_token", accessToken)
	parsed.RawQuery = query.Encode()
	return p.postJSON(ctx, parsed.String(), payload, out)
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
	if apiErr := newDingTalkHTTPError(endpoint, resp.StatusCode, body); apiErr != nil {
		return apiErr
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode DingTalk OAuth response: %w", err)
	}
	return nil
}
