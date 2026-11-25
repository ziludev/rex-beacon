package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

// FeishuWebhookNotifier sends interactive cards via a Feishu/Lark custom bot webhook.
type FeishuWebhookNotifier struct {
	cfg        config.WebhookConfig
	httpClient *http.Client
}

// NewFeishuWebhookNotifier creates a webhook notifier.
func NewFeishuWebhookNotifier(cfg config.WebhookConfig) (*FeishuWebhookNotifier, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook requires url")
	}
	return &FeishuWebhookNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (w *FeishuWebhookNotifier) Name() string {
	return "feishu_webhook"
}

// SignWebhook computes the Feishu custom bot HMAC-SHA256 signature.
func SignWebhook(secret string, timestamp int64) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Send posts an interactive card to the configured webhook URL.
func (w *FeishuWebhookNotifier) Send(ctx context.Context, n Notification) error {
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     BuildFeishuCard(n),
	}
	if w.cfg.Secret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = SignWebhook(w.cfg.Secret, ts)
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(rawBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var res struct {
		Code       int    `json:"code"`
		StatusCode int    `json:"StatusCode"`
		Msg        string `json:"msg"`
		StatusMsg  string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(rawResp, &res); err != nil {
		if resp.StatusCode >= 300 {
			return fmt.Errorf("webhook http %d: %s", resp.StatusCode, string(rawResp))
		}
		return nil
	}
	if res.Code != 0 || res.StatusCode != 0 {
		msg := res.Msg
		if msg == "" {
			msg = res.StatusMsg
		}
		return fmt.Errorf("webhook error (%d/%d): %s", res.Code, res.StatusCode, msg)
	}
	return nil
}
