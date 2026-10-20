package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// 2026-10-20 coder(lq): Application-token contract from Alibaba's official
// @alicloud/dingtalk contact_1_0 ListEmpLeaveRecords SDK. Retain only stable
// user IDs; names, phone numbers and resignation reasons are not needed.
func (p DingTalkOAuthProvider) LoadDepartedUserIDs(ctx context.Context, start, end time.Time) ([]string, error) {
	start, end = start.UTC().Truncate(time.Second), end.UTC().Truncate(time.Second)
	if start.IsZero() || !start.Before(end) {
		return nil, errors.New("invalid departure query range")
	}
	token, err := p.directoryAppToken(ctx)
	if err != nil {
		return nil, err
	}
	limiter := p.directoryLimiter()
	endpoint := p.DepartureListURL
	if endpoint == "" {
		endpoint = dingtalkAPIBase + "/v1.0/contact/empLeaveRecords"
	}
	users := map[string]bool{}
	for from := start; from.Before(end); {
		until := from.Add(30 * 24 * time.Hour)
		if until.After(end) {
			until = end
		}
		cursor := ""
		seen := map[string]bool{}
		for page := 0; ; page++ {
			if page >= 1000 {
				return nil, errors.New("departure pagination limit exceeded")
			}
			target, err := url.Parse(endpoint)
			if err != nil {
				return nil, err
			}
			query := target.Query()
			query.Set("startTime", from.Format(time.RFC3339))
			query.Set("endTime", until.Format(time.RFC3339))
			query.Set("maxResults", "50")
			if cursor != "" {
				query.Set("nextToken", cursor)
			}
			target.RawQuery = query.Encode()
			var response struct {
				Records *[]struct {
					UserID    string `json:"userId"`
					LeaveTime string `json:"leaveTime"`
				} `json:"records"`
				NextToken string `json:"nextToken"`
			}
			if err := p.getDeparturePage(ctx, target.String(), token, limiter, &response); err != nil {
				return nil, err
			}
			if response.Records == nil {
				return nil, errors.New("departure response is missing records")
			}
			for _, record := range *response.Records {
				at, err := time.Parse(time.RFC3339, record.LeaveTime)
				if err != nil || record.UserID == "" || at.Before(from) || at.After(until) {
					return nil, errors.New("departure response contains an invalid record")
				}
				users[record.UserID] = true
			}
			if response.NextToken == "" {
				break
			}
			if seen[response.NextToken] {
				return nil, errors.New("departure cursor did not advance")
			}
			seen[response.NextToken] = true
			cursor = response.NextToken
		}
		from = until
	}
	ids := make([]string, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (p DingTalkOAuthProvider) directoryAppToken(ctx context.Context) (string, error) {
	endpoint := p.AppTokenURL
	if endpoint == "" {
		endpoint = dingtalkAPIBase + "/v1.0/oauth2/accessToken"
	}
	payload, _ := json.Marshal(map[string]string{"appKey": p.ClientID, "appSecret": p.ClientSecret})
	var response struct {
		AccessToken string `json:"accessToken"`
	}
	if err := p.postJSON(ctx, endpoint, payload, &response); err != nil {
		return "", newDingTalkDirectorySyncError("application_token", "", 0, "", err)
	}
	if response.AccessToken == "" {
		return "", newDingTalkDirectorySyncError("application_token", "", 0, "DingTalk application token response missing accessToken", nil)
	}
	return response.AccessToken, nil
}

func (p DingTalkOAuthProvider) directoryLimiter() *dingtalkDirectoryLimiter {
	interval := p.DirectoryRequestInterval
	if interval < 0 {
		interval = 0
	} else if interval == 0 {
		interval = defaultDingTalkDirectoryRequestInterval
	}
	return &dingtalkDirectoryLimiter{interval: interval}
}

func (p DingTalkOAuthProvider) getDeparturePage(ctx context.Context, endpoint, token string, limiter *dingtalkDirectoryLimiter, out any) error {
	delays := p.DirectoryRetryDelays
	if delays == nil {
		delays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		if err := limiter.wait(ctx); err != nil {
			return err
		}
		err := p.getDepartureJSON(ctx, endpoint, token, out)
		if err == nil || attempt >= len(delays) || !isDingTalkDirectoryRetryable(err) {
			return err
		}
		if err := waitDingTalkDirectoryRetry(ctx, delays[attempt]); err != nil {
			return err
		}
	}
}

func (p DingTalkOAuthProvider) getDepartureJSON(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := newDingTalkHTTPError(endpoint, resp.StatusCode, body); err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode departure response: %w", err)
	}
	return nil
}
