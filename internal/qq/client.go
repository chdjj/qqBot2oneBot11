// Package qq implements a minimal client for the QQ official bot API v2.
package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const tokenURL = "https://bots.qq.com/app/getAppAccessToken"

func baseURLOverride() string { return strings.TrimSpace(os.Getenv("QQBOT_API_BASE")) }

func tokenOverride() string { return strings.TrimSpace(os.Getenv("QQBOT_TOKEN_URL")) }

// APIError is an error returned by the QQ open platform.
type APIError struct {
	Status  int    `json:"-"`
	Code    int    `json:"code"`
	ErrCode int    `json:"err_code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

func (e *APIError) Error() string {
	code := e.Code
	if code == 0 {
		code = e.ErrCode
	}
	if hint := authErrorHint(code); hint != "" {
		if e.Message == "" {
			return hint
		}
		return e.Message + "（" + hint + "）"
	}
	if e.Message == "" {
		return fmt.Sprintf("qq api error: http %d", e.Status)
	}
	return fmt.Sprintf("qq api error: %s (code %d, http %d)", e.Message, code, e.Status)
}

// Client talks to a single bot application.
type Client struct {
	AppID   string
	Secret  string
	Sandbox bool

	hc *http.Client

	mu           sync.Mutex
	token        string
	expires      time.Time
	baseOverride string
}

func NewClient(appID, secret string, sandbox bool) *Client {
	return &Client{
		AppID:   appID,
		Secret:  secret,
		Sandbox: sandbox,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) BaseURL() string {
	c.mu.Lock()
	override := c.baseOverride
	c.mu.Unlock()
	if override != "" {
		return override
	}
	// QQBOT_API_BASE lets the test harness point the client at a mock upstream.
	if v := baseURLOverride(); v != "" {
		return v
	}
	if c.Sandbox {
		return "https://sandbox.api.sgroup.qq.com"
	}
	return "https://api.sgroup.qq.com"
}

func (c *Client) httpClient() *http.Client { return c.hc }

// SetHTTPClient lets callers share or tune the transport (used for tests).
func (c *Client) SetHTTPClient(hc *http.Client) { c.hc = hc }

// SetBaseURL overrides the API base for tests against a local server.
func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	c.baseOverride = strings.TrimSuffix(u, "/")
	c.mu.Unlock()
}

// forceToken installs a prepared access token so tests skip the token exchange.
func (c *Client) forceToken(tok string) {
	c.mu.Lock()
	c.token = tok
	c.expires = time.Now().Add(time.Hour)
	c.mu.Unlock()
}

// Token returns a cached access token, refreshing it when it is close to expiry.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"appId": c.AppID, "clientSecret": c.Secret})
	tu := tokenURL
	if v := tokenOverride(); v != "" {
		tu = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tu, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
		Code        int    `json:"code"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("get token: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if out.AccessToken == "" {
		return "", &APIError{Status: resp.StatusCode, Code: out.Code, Message: firstNonEmpty(out.Message, "获取 access_token 失败："+truncate(string(raw), 200))}
	}
	ttl := 7200 * time.Second
	switch v := out.ExpiresIn.(type) {
	case float64:
		ttl = time.Duration(v) * time.Second
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			ttl = time.Duration(n) * time.Second
		}
	}
	if ttl <= 0 {
		ttl = 7200 * time.Second
	}
	c.token = out.AccessToken
	// Refresh with margin so long-lived calls never use a dying token.
	c.expires = time.Now().Add(ttl - 120*time.Second)
	return c.token, nil
}

// Invalidate drops the cached token, e.g. after a 401.
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// Do performs an authenticated request against the bot API. Path must start with "/".
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	return c.do(ctx, method, path, body, out, false)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, retried bool) error {
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+tok)
	req.Header.Set("X-Union-Appid", c.AppID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized && !retried {
		c.Invalidate()
		return c.do(ctx, method, path, body, out, true)
	}
	if resp.StatusCode >= 400 {
		ae := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, ae)
		if ae.Code == 0 && ae.ErrCode == 0 {
			ae.Message = truncate(string(raw), 300)
		}
		return ae
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w: %s", err, truncate(string(raw), 300))
		}
	}
	return nil
}

// GatewayURL fetches the WebSocket gateway endpoint.
func (c *Client) GatewayURL(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.Do(ctx, http.MethodGet, "/gateway", nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("gateway url empty")
	}
	return out.URL, nil
}

// Message is the payload accepted by the group and C2C message endpoints.
type Message struct {
	MsgType  int    `json:"msg_type"`
	Content  string `json:"content,omitempty"`
	Markdown any    `json:"markdown,omitempty"`
	Media    *Media `json:"media,omitempty"`
	MsgID    string `json:"msg_id,omitempty"`
	MsgSeq   *int   `json:"msg_seq,omitempty"`
	EventID  string `json:"event_id,omitempty"`
}

type Media struct {
	FileInfo string `json:"file_info"`
}

type SendResult struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// SendGroup sends a text (or media) message to a group.
func (c *Client) SendGroup(ctx context.Context, groupOpenID string, m *Message) (*SendResult, error) {
	var out SendResult
	err := c.Do(ctx, http.MethodPost, "/v2/groups/"+url.PathEscape(groupOpenID)+"/messages", m, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SendC2C sends a text (or media) message to a single user.
func (c *Client) SendC2C(ctx context.Context, userOpenID string, m *Message) (*SendResult, error) {
	var out SendResult
	err := c.Do(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(userOpenID)+"/messages", m, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadResult describes a media upload.
type UploadResult struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
	ID       string `json:"id"`
}

// MediaUpload describes media to register before sending.
type MediaUpload struct {
	FileType int    // 1 image, 2 video, 3 voice, 4 file
	URL      string // remote url; the platform downloads it
	Data     string // base64 payload for direct upload (<=5MB), wins over URL
	Name     string // file name, required for direct uploads
}

func (m MediaUpload) Body() map[string]any {
	b := map[string]any{"file_type": m.FileType, "srv_send_msg": false}
	if m.Data != "" {
		b["file_data"] = m.Data
		b["file_name"] = m.Name
	} else {
		b["url"] = m.URL
	}
	return b
}

// UploadGroupMedia registers media for later sending in a group.
func (c *Client) UploadGroupMedia(ctx context.Context, groupOpenID string, m MediaUpload) (*UploadResult, error) {
	var out UploadResult
	if err := c.Do(ctx, http.MethodPost, "/v2/groups/"+url.PathEscape(groupOpenID)+"/files", m.Body(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadC2CMedia registers media for later sending in a C2C chat.
func (c *Client) UploadC2CMedia(ctx context.Context, userOpenID string, m MediaUpload) (*UploadResult, error) {
	var out UploadResult
	if err := c.Do(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(userOpenID)+"/files", m.Body(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetractGroupMessage recalls a message sent by the bot.
func (c *Client) RetractGroupMessage(ctx context.Context, groupOpenID, msgID string) error {
	return c.Do(ctx, http.MethodDelete, "/v2/groups/"+url.PathEscape(groupOpenID)+"/messages/"+url.PathEscape(msgID), nil, nil)
}

// RetractC2CMessage recalls a C2C message sent by the bot.
func (c *Client) RetractC2CMessage(ctx context.Context, userOpenID, msgID string) error {
	return c.Do(ctx, http.MethodDelete, "/v2/users/"+url.PathEscape(userOpenID)+"/messages/"+url.PathEscape(msgID), nil, nil)
}

// GroupInfo fetches group metadata (whitelist-only endpoint).
func (c *Client) GroupInfo(ctx context.Context, groupOpenID string) (map[string]any, error) {
	var out map[string]any
	err := c.Do(ctx, http.MethodGet, "/v2/groups/"+url.PathEscape(groupOpenID)+"/info", nil, &out)
	return out, err
}

// SelfInfo fetches the bot account itself, used to validate credentials.
func (c *Client) SelfInfo(ctx context.Context) (id, username string, err error) {
	var out struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	}
	if err := c.Do(ctx, http.MethodGet, "/users/@me", nil, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.Username, nil
}

// authErrorHint explains the platform's credential error codes in Chinese so
// the console can show something actionable.
func authErrorHint(code int) string {
	switch code {
	case 100007:
		return "AppID 无效，请检查开放平台的 AppID"
	case 100010:
		return "AppSecret 无效或与 AppID 不匹配"
	case 100015:
		return "access_token 无效或已过期"
	case 100016:
		return "access_token 已过期，正在尝试刷新"
	case 10004:
		return "机器人不存在或凭据不正确"
	case 11241:
		return "机器人已被禁用"
	case 11242:
		return "机器人已被封禁"
	case 11243:
		return "机器人未上线或不支持该能力"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
