package qq

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

const (
	apiURL     = "https://api.sgroup.qq.com"
	sandboxURL = "https://sandbox.api.sgroup.qq.com"
	tokenURL   = "https://bots.qq.com/app/getAppAccessToken"
)

// client 独占一个账号的令牌；刷新锁避免并发请求分别刷新同一凭证。
type client struct {
	http                   *http.Client
	appID, secret, baseURL string
	mu                     sync.Mutex
	token                  string
	expires                time.Time
}

// apiError 保留 QQ 原始错误体与 trace，发送层据此区分明确拒绝与结果未知。
type apiError struct {
	status      int
	code        int
	body, trace string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("QQ API HTTP %d code %d trace %s: %s", e.status, e.code, e.trace, e.body)
}

func (c *client) safe(err error) error {
	if err == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	text := strings.ReplaceAll(err.Error(), c.secret, "[redacted]")
	if c.token != "" {
		text = strings.ReplaceAll(text, c.token, "[redacted]")
	}
	return errors.New(text)
}

func (c *client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	payload, err := json.Marshal(map[string]string{"appId": c.appID, "clientSecret": c.secret})
	if err != nil {
		return "", err
	}
	var response struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
	}
	if err := c.request(ctx, http.MethodPost, tokenURL, "", "application/json", bytes.NewReader(payload), &response); err != nil {
		return "", err
	}
	seconds, err := response.ExpiresIn.Int64()
	if err != nil || seconds <= 0 || response.AccessToken == "" {
		return "", errors.New("invalid QQ access token response")
	}
	c.token, c.expires = response.AccessToken, time.Now().Add(time.Duration(seconds)*time.Second)
	return c.token, nil
}

func (c *client) call(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	return c.upload(ctx, method, path, "application/json", body, out)
}

func (c *client) upload(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	return c.request(ctx, method, c.baseURL+path, token, contentType, body, out)
}

func (c *client) request(ctx context.Context, method, address, token, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "QQBot "+token)
		req.Header.Set("X-Union-Appid", c.appID)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return fmt.Errorf("decode QQ response: %w", err)
			}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != 0 {
		return &apiError{status: response.StatusCode, code: envelope.Code, body: string(encoded), trace: response.Header.Get("X-Tps-Trace-Id")}
	}
	if out != nil {
		if err := json.Unmarshal(encoded, out); err != nil {
			return fmt.Errorf("decode QQ response: %w", err)
		}
	}
	return nil
}
