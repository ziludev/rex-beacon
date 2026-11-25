package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

// FeishuAppNotifier sends interactive cards via Feishu/Lark OpenAPI using app credentials.
type FeishuAppNotifier struct {
	cfg        config.FeishuAppConfig
	httpClient *http.Client
	baseURL    string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewFeishuAppNotifier validates cfg and returns a FeishuAppNotifier.
func NewFeishuAppNotifier(cfg config.FeishuAppConfig) (*FeishuAppNotifier, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" || cfg.ReceiveID == "" {
		return nil, fmt.Errorf("feishu_app requires app_id, app_secret, and receive_id")
	}
	baseURL := "https://open.feishu.cn"
	if strings.EqualFold(cfg.Brand, "lark") {
		baseURL = "https://open.larksuite.com"
	}
	if cfg.ReceiveIDType == "" {
		cfg.ReceiveIDType = "open_id"
	}
	return &FeishuAppNotifier{
		cfg:        cfg,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (f *FeishuAppNotifier) Name() string {
	if strings.EqualFold(f.cfg.Brand, "lark") {
		return "lark_app"
	}
	return "feishu_app"
}

func (f *FeishuAppNotifier) getToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	if f.token != "" && time.Now().Add(2*time.Minute).Before(f.expiresAt) {
		tok := f.token
		f.mu.Unlock()
		return tok, nil
	}
	f.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{
		"app_id":     f.cfg.AppID,
		"app_secret": f.cfg.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var parsed struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if parsed.Code != 0 || parsed.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu auth error (%d): %s", parsed.Code, parsed.Msg)
	}

	ttl := time.Duration(parsed.Expire) * time.Second
	if ttl <= 0 {
		ttl = 90 * time.Minute
	}
	f.mu.Lock()
	f.token = parsed.TenantAccessToken
	f.expiresAt = time.Now().Add(ttl)
	f.mu.Unlock()
	return parsed.TenantAccessToken, nil
}

// Send sends an interactive card message to the configured Feishu/Lark recipient.
func (f *FeishuAppNotifier) Send(ctx context.Context, n Notification) error {
	token, err := f.getToken(ctx)
	if err != nil {
		return err
	}

	cardBytes, err := json.Marshal(BuildFeishuCard(n))
	if err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(map[string]string{
		"receive_id": f.cfg.ReceiveID,
		"msg_type":   "interactive",
		"content":    string(cardBytes),
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s", f.baseURL, f.cfg.ReceiveIDType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("http %d decode message response: %w", resp.StatusCode, err)
	}
	if res.Code != 0 {
		// Invalidate token if auth-related error so next call refreshes.
		if res.Code == 99991663 || res.Code == 99991664 || res.Code == 99991661 {
			f.mu.Lock()
			f.token = ""
			f.mu.Unlock()
		}
		return fmt.Errorf("feishu send error (%d): %s", res.Code, res.Msg)
	}
	return nil
}
