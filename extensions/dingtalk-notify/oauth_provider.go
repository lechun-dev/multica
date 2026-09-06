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
	"sync"
	"time"
)

const defaultDingTalkDirectoryRequestInterval = 100 * time.Millisecond

// 2026-09-07 coder(lq): Keep directory API calls below DingTalk's aggregate
// QPS limit and make retry timing injectable for fast deterministic tests.
type dingtalkDirectoryLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (l *dingtalkDirectoryLimiter) wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return nil
	}
	now := time.Now()
	l.mu.Lock()
	start := l.next
	if start.Before(now) {
		start = now
	}
	l.next = start.Add(l.interval)
	delay := start.Sub(now)
	l.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

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
	DepartmentListURL   string
	UserListURL         string
	ClientID            string
	ClientSecret        string
	Scope               string
	CorpID              string
	// DirectoryRequestInterval defaults to 100ms. Set it below zero only for
	// controlled environments that provide their own QPS protection.
	DirectoryRequestInterval time.Duration
	// DirectoryRetryDelays overrides the default 1s/2s/4s rate-limit backoff.
	DirectoryRetryDelays []time.Duration
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
		if enterpriseIdentity.Email != "" {
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

type dingTalkSubDepartment struct {
	ID       dingTalkID `json:"dept_id"`
	Name     string     `json:"name"`
	ParentID dingTalkID `json:"parent_id"`
}

// 2026-09-05 coder(lq): DingTalk returns department `result` as an array on
// some tenant/API versions and as {"list": [...]} on others. Accept both
// wire shapes so a tenant-specific response does not abort a full sync.
type dingTalkDepartmentListResult struct {
	List []dingTalkSubDepartment
}

func (r *dingTalkDepartmentListResult) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		r.List = nil
		return nil
	}
	if data[0] == '[' {
		return json.Unmarshal(data, &r.List)
	}
	var wrapped struct {
		List []dingTalkSubDepartment `json:"list"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return err
	}
	r.List = wrapped.List
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

// 2026-09-03 coder(lq): Read the provider snapshot before opening any host
// transaction, so a DingTalk outage cannot erase the last known directory.
// LoadDirectory returns a complete DingTalk enterprise directory rooted at
// department 1. The host persists the snapshot transactionally, so this
// method never mutates Multica data itself.
func (p DingTalkOAuthProvider) LoadDirectory(ctx context.Context) (DingTalkDirectorySnapshot, error) {
	tokenURL := p.AppTokenURL
	if tokenURL == "" {
		tokenURL = dingtalkAPIBase + "/v1.0/oauth2/accessToken"
	}
	payload, _ := json.Marshal(map[string]string{"appKey": p.ClientID, "appSecret": p.ClientSecret})
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if err := p.postJSON(ctx, tokenURL, payload, &token); err != nil {
		return DingTalkDirectorySnapshot{}, newDingTalkDirectorySyncError("application_token", "", 0, "", err)
	}
	if token.AccessToken == "" {
		return DingTalkDirectorySnapshot{}, newDingTalkDirectorySyncError("application_token", "", 0, "DingTalk application token response missing accessToken", nil)
	}
	interval := p.DirectoryRequestInterval
	if interval < 0 {
		interval = 0
	} else if interval == 0 {
		interval = defaultDingTalkDirectoryRequestInterval
	}
	limiter := &dingtalkDirectoryLimiter{interval: interval}

	departments := make([]DingTalkDirectoryDepartment, 0)
	departmentIDs := make(map[string]struct{})
	seen := map[string]bool{}
	queue := []string{"1"}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		dept, err := p.loadDingTalkSubDepartments(ctx, token.AccessToken, id, limiter)
		if err != nil {
			return DingTalkDirectorySnapshot{}, err
		}
		for _, d := range dept {
			if d.ID == "" {
				continue
			}
			if _, exists := departmentIDs[d.ID]; exists {
				continue
			}
			departmentIDs[d.ID] = struct{}{}
			departments = append(departments, d)
			queue = append(queue, d.ID)
		}
	}

	membersByID := map[string]DingTalkDirectoryMember{}
	for _, d := range append([]DingTalkDirectoryDepartment{{ID: "1"}}, departments...) {
		members, err := p.loadDingTalkDepartmentUsers(ctx, token.AccessToken, d.ID, limiter)
		if err != nil {
			return DingTalkDirectorySnapshot{}, err
		}
		for _, m := range members {
			if m.DingUserID == "" {
				continue
			}
			if existing, ok := membersByID[m.DingUserID]; ok {
				seenDept := map[string]bool{}
				for _, x := range existing.DepartmentIDs {
					seenDept[x] = true
				}
				for _, x := range m.DepartmentIDs {
					if !seenDept[x] {
						existing.DepartmentIDs = append(existing.DepartmentIDs, x)
					}
				}
				if existing.Name == "" {
					existing.Name, existing.Email, existing.UnionID = m.Name, m.Email, m.UnionID
				}
				membersByID[m.DingUserID] = existing
			} else {
				membersByID[m.DingUserID] = m
			}
		}
	}
	members := make([]DingTalkDirectoryMember, 0, len(membersByID))
	for _, m := range membersByID {
		members = append(members, m)
	}
	return DingTalkDirectorySnapshot{Departments: departments, Members: members}, nil
}

func (p DingTalkOAuthProvider) loadDingTalkSubDepartments(ctx context.Context, token, parentID string, limiter *dingtalkDirectoryLimiter) ([]DingTalkDirectoryDepartment, error) {
	endpoint := p.DepartmentListURL
	if endpoint == "" {
		endpoint = "https://oapi.dingtalk.com/topapi/v2/department/listsub"
	}
	var id any = parentID
	if n, err := strconv.ParseInt(parentID, 10, 64); err == nil {
		id = n
	}
	payload, _ := json.Marshal(map[string]any{"dept_id": id, "language": "zh_CN"})
	var response struct {
		ErrCode int                          `json:"errcode"`
		ErrMsg  string                       `json:"errmsg"`
		Result  dingTalkDepartmentListResult `json:"result"`
	}
	if err := p.postDirectoryAppJSON(ctx, endpoint, token, payload, &response, limiter); err != nil {
		return nil, newDingTalkDirectorySyncError("departments", parentID, 0, "", err)
	}
	if response.ErrCode != 0 {
		return nil, newDingTalkDirectorySyncError("departments", parentID, response.ErrCode, response.ErrMsg, nil)
	}
	out := make([]DingTalkDirectoryDepartment, 0, len(response.Result.List))
	for _, d := range response.Result.List {
		out = append(out, DingTalkDirectoryDepartment{ID: strings.TrimSpace(string(d.ID)), Name: strings.TrimSpace(d.Name), ParentID: strings.TrimSpace(string(d.ParentID))})
	}
	return out, nil
}

func (p DingTalkOAuthProvider) loadDingTalkDepartmentUsers(ctx context.Context, token, departmentID string, limiter *dingtalkDirectoryLimiter) ([]DingTalkDirectoryMember, error) {
	endpoint := p.UserListURL
	if endpoint == "" {
		endpoint = "https://oapi.dingtalk.com/topapi/v2/user/list"
	}
	var id any = departmentID
	if n, err := strconv.ParseInt(departmentID, 10, 64); err == nil {
		id = n
	}
	users := make([]DingTalkDirectoryMember, 0)
	var cursor int64
	for {
		payload, _ := json.Marshal(map[string]any{"dept_id": id, "cursor": cursor, "size": 100, "language": "zh_CN"})
		var response struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
			Result  struct {
				List []struct {
					UserID  string       `json:"userid"`
					UnionID string       `json:"unionid"`
					Name    string       `json:"name"`
					Email   string       `json:"email"`
					DeptIDs []dingTalkID `json:"dept_id_list"`
				} `json:"list"`
				NextCursor int64 `json:"next_cursor"`
				HasMore    bool  `json:"has_more"`
			} `json:"result"`
		}
		if err := p.postDirectoryAppJSON(ctx, endpoint, token, payload, &response, limiter); err != nil {
			return nil, newDingTalkDirectorySyncError("department_users", departmentID, 0, "", err)
		}
		if response.ErrCode != 0 {
			return nil, newDingTalkDirectorySyncError("department_users", departmentID, response.ErrCode, response.ErrMsg, nil)
		}
		for _, u := range response.Result.List {
			deptIDs := make([]string, 0, len(u.DeptIDs))
			for _, d := range u.DeptIDs {
				deptIDs = append(deptIDs, strings.TrimSpace(string(d)))
			}
			users = append(users, DingTalkDirectoryMember{DingUserID: strings.TrimSpace(u.UserID), UnionID: strings.TrimSpace(u.UnionID), Name: strings.TrimSpace(u.Name), Email: strings.ToLower(strings.TrimSpace(u.Email)), DepartmentIDs: deptIDs})
		}
		if !response.Result.HasMore {
			break
		}
		if response.Result.NextCursor == cursor {
			return nil, newDingTalkDirectorySyncError("department_users", departmentID, 0, "DingTalk user list cursor did not advance", nil)
		}
		cursor = response.Result.NextCursor
	}
	return users, nil
}

func (p DingTalkOAuthProvider) postDirectoryAppJSON(ctx context.Context, endpoint, accessToken string, payload []byte, out any, limiter *dingtalkDirectoryLimiter) error {
	delays := p.DirectoryRetryDelays
	if delays == nil {
		delays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		if err := limiter.wait(ctx); err != nil {
			return err
		}
		err := p.postAppJSON(ctx, endpoint, accessToken, payload, out)
		if err == nil || attempt >= len(delays) || !isDingTalkDirectoryRetryable(err) {
			return err
		}
		if err := waitDingTalkDirectoryRetry(ctx, delays[attempt]); err != nil {
			return err
		}
	}
}

func isDingTalkDirectoryRetryable(err error) bool {
	var apiErr *DingTalkHTTPError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500 {
		return true
	}
	if apiErr.Code != "errcode:88" {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return message == "" ||
		strings.Contains(message, "qps") ||
		strings.Contains(message, "流控") ||
		strings.Contains(message, "次数过多") ||
		strings.Contains(message, "rate limit") ||
		strings.Contains(message, "too many") ||
		strings.Contains(message, "throttl")
}

func waitDingTalkDirectoryRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
