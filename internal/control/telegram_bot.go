package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

// TelegramBot polls the Telegram Bot API (`getUpdates`) to handle inline button
// callbacks (`callback_query`) and chat commands/replies (`message`).
type TelegramBot struct {
	cfg        config.TelegramConfig
	baseURL    string
	ctrl       *Controller
	httpClient *http.Client
	log        *log.Logger
	offset     int64
}

// NewTelegramBot creates a TelegramBot long-polling runner.
func NewTelegramBot(cfg config.TelegramConfig, ctrl *Controller, logger *log.Logger) *TelegramBot {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &TelegramBot{
		cfg:        cfg,
		baseURL:    base,
		ctrl:       ctrl,
		httpClient: &http.Client{Timeout: 35 * time.Second},
		log:        logger,
	}
}

// Run polls Telegram updates until ctx is cancelled.
func (t *TelegramBot) Run(ctx context.Context) error {
	t.log.Printf("telegram interactive polling starting")
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := t.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			t.log.Printf("telegram poll error: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgMessage struct {
	MessageID      int64      `json:"message_id"`
	From           *tgUser    `json:"from"`
	Chat           tgChat     `json:"chat"`
	Text           string     `json:"text"`
	ReplyToMessage *tgMessage `json:"reply_to_message"`
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

func (t *TelegramBot) pollOnce(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates", t.baseURL, t.cfg.BotToken)
	body, _ := json.Marshal(map[string]any{
		"offset":          t.offset,
		"timeout":         25,
		"allowed_updates": []string{"message", "callback_query"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	var parsed struct {
		OK          bool       `json:"ok"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	if !parsed.OK {
		return fmt.Errorf("getUpdates failed: %s", parsed.Description)
	}
	for _, upd := range parsed.Result {
		if upd.UpdateID >= t.offset {
			t.offset = upd.UpdateID + 1
		}
		t.handleUpdate(ctx, upd)
	}
	return nil
}

func (t *TelegramBot) handleUpdate(ctx context.Context, upd tgUpdate) {
	if upd.CallbackQuery != nil {
		t.handleCallback(ctx, upd.CallbackQuery)
	}
	if upd.Message != nil && strings.TrimSpace(upd.Message.Text) != "" {
		t.handleMessage(ctx, upd.Message)
	}
}

func (t *TelegramBot) handleCallback(ctx context.Context, cq *tgCallbackQuery) {
	chatID := ""
	var replyMsgID int64
	if cq.Message != nil {
		chatID = strconv.FormatInt(cq.Message.Chat.ID, 10)
		replyMsgID = cq.Message.MessageID
	}
	if !t.isAuthorized(chatID, cq.From.ID) {
		_ = t.answerCallback(ctx, cq.ID, "无权限操作该终端")
		return
	}

	action, blockID, text, label := parseTelegramCallback(cq.Data)
	if action == "" {
		_ = t.answerCallback(ctx, cq.ID, "无效操作")
		return
	}
	res := t.ctrl.HandleButtonAction(ctx, action, blockID, text, label)
	_ = t.answerCallback(ctx, cq.ID, res.Toast)
	if !res.IsError && res.DetailText != "" && chatID != "" {
		_ = t.sendHTML(ctx, chatID, replyMsgID, "<pre>"+html.EscapeString(res.DetailText)+"</pre>")
	}
}

func (t *TelegramBot) handleMessage(ctx context.Context, msg *tgMessage) {
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	var senderID int64
	if msg.From != nil {
		senderID = msg.From.ID
	}
	if !t.isAuthorized(chatID, senderID) {
		return
	}

	replyToRef := ""
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.MessageID > 0 {
		replyToRef = "tg:" + strconv.FormatInt(msg.ReplyToMessage.MessageID, 10)
	}
	reply := t.ctrl.HandleTextCommand(ctx, msg.Text, replyToRef)
	if reply != "" {
		_ = t.sendHTML(ctx, chatID, msg.MessageID, html.EscapeString(reply))
	}
}

func parseTelegramCallback(data string) (action, blockID, text, label string) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || parts[0] != "b" {
		return "", "", "", ""
	}
	short := parts[1]
	switch parts[2] {
	case "y":
		return "reply", short, "y\r", "y"
	case "1":
		return "reply", short, "1\r", "1"
	case "n":
		return "reply", short, "n\r", "n"
	case "ctrlc":
		return "reply", short, "\x03", "Ctrl+C"
	case "peek":
		return "peek", short, "", ""
	default:
		return "", "", "", ""
	}
}

func (t *TelegramBot) isAuthorized(chatID string, userID int64) bool {
	if strings.TrimSpace(t.cfg.ChatID) != "" && chatID != strings.TrimSpace(t.cfg.ChatID) {
		return false
	}
	if len(t.cfg.AllowedUsers) > 0 {
		uidStr := strconv.FormatInt(userID, 10)
		for _, allowed := range t.cfg.AllowedUsers {
			if strings.TrimSpace(allowed) == uidStr {
				return true
			}
		}
		return false
	}
	return true
}

func (t *TelegramBot) answerCallback(ctx context.Context, callbackID, text string) error {
	endpoint := fmt.Sprintf("%s/bot%s/answerCallbackQuery", t.baseURL, t.cfg.BotToken)
	body, _ := json.Marshal(map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	})
	return t.postTG(ctx, endpoint, body)
}

func (t *TelegramBot) sendHTML(ctx context.Context, chatID string, replyTo int64, htmlBody string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.cfg.BotToken)
	payload := map[string]any{
		"chat_id":                  chatID,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"text":                     htmlBody,
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	body, _ := json.Marshal(payload)
	return t.postTG(ctx, endpoint, body)
}

func (t *TelegramBot) postTG(ctx context.Context, endpoint string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram http %d", resp.StatusCode)
	}
	return nil
}
