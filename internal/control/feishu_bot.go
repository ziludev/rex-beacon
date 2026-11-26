package control

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/ziludev/rex-beacon/internal/config"
	"github.com/ziludev/rex-beacon/internal/notify"
)

// FeishuBot maintains a Feishu/Lark WebSocket long connection to handle interactive
// card button callbacks (`card.action.trigger`) and incoming chat replies/commands (`im.message.receive_v1`).
type FeishuBot struct {
	cfg      config.FeishuAppConfig
	ctrl     *Controller
	notifier *notify.FeishuAppNotifier
	log      *log.Logger
}

// NewFeishuBot creates a Feishu WebSocket interactive bot runner.
func NewFeishuBot(cfg config.FeishuAppConfig, ctrl *Controller, notifier *notify.FeishuAppNotifier, logger *log.Logger) *FeishuBot {
	return &FeishuBot{
		cfg:      cfg,
		ctrl:     ctrl,
		notifier: notifier,
		log:      logger,
	}
}

// Run connects to the Feishu/Lark WebSocket endpoint and blocks until ctx is cancelled.
func (b *FeishuBot) Run(ctx context.Context) error {
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2CardActionTrigger(func(evCtx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return b.handleCardAction(evCtx, event)
		}).
		OnP2MessageReceiveV1(func(evCtx context.Context, event *larkim.P2MessageReceiveV1) error {
			return b.handleMessage(evCtx, event)
		})

	domain := "https://open.feishu.cn"
	if strings.EqualFold(b.cfg.Brand, "lark") {
		domain = "https://open.larksuite.com"
	}

	b.log.Printf("feishu interactive websocket starting (brand=%s)", b.cfg.Brand)
	cli := larkws.NewClient(
		b.cfg.AppID,
		b.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithDomain(domain),
	)
	return cli.Start(ctx)
}

func (b *FeishuBot) handleCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, nil
	}
	var openID, userID, chatID, msgID string
	if event.Event.Operator != nil {
		openID = event.Event.Operator.OpenID
		if event.Event.Operator.UserID != nil {
			userID = *event.Event.Operator.UserID
		}
	}
	if event.Event.Context != nil {
		chatID = event.Event.Context.OpenChatID
		msgID = event.Event.Context.OpenMessageID
	}
	if !b.isAuthorized(openID, userID, chatID) {
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "error", Content: "无权限操作该终端"},
		}, nil
	}

	val := event.Event.Action.Value
	action, _ := val["action"].(string)
	blockID, _ := val["block_id"].(string)
	text, _ := val["text"].(string)
	label, _ := val["label"].(string)

	runCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	res := b.ctrl.HandleButtonAction(runCtx, action, blockID, text, label)
	if res.IsError {
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "error", Content: res.Toast},
		}, nil
	}

	if res.DetailText != "" && b.notifier != nil {
		go func(replyMsgID, replyChatID, body string) {
			sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer sendCancel()
			if err := b.notifier.ReplyText(sendCtx, replyMsgID, replyChatID, body); err != nil {
				b.log.Printf("feishu peek reply error: %v", err)
			}
		}(msgID, chatID, res.DetailText)
	}

	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: res.Toast},
	}, nil
}

func (b *FeishuBot) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	if event.Event.Sender != nil && event.Event.Sender.SenderType != nil && *event.Event.Sender.SenderType == "bot" {
		return nil
	}

	var openID, userID string
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		if event.Event.Sender.SenderId.OpenId != nil {
			openID = *event.Event.Sender.SenderId.OpenId
		}
		if event.Event.Sender.SenderId.UserId != nil {
			userID = *event.Event.Sender.SenderId.UserId
		}
	}
	msg := event.Event.Message
	chatID := derefStr(msg.ChatId)
	if !b.isAuthorized(openID, userID, chatID) {
		return nil
	}
	if derefStr(msg.MessageType) != "text" {
		return nil
	}

	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(derefStr(msg.Content)), &content); err != nil {
		return nil
	}

	parentID := derefStr(msg.ParentId)
	if parentID == "" {
		parentID = derefStr(msg.RootId)
	}
	msgID := derefStr(msg.MessageId)

	// Process asynchronously so Feishu's 3-second WS ack deadline is never exceeded by Rex CLI calls.
	go func(text, replyToRef, replyMsgID, targetChatID string) {
		runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reply := b.ctrl.HandleTextCommand(runCtx, text, replyToRef)
		if reply != "" && b.notifier != nil {
			if err := b.notifier.ReplyText(runCtx, replyMsgID, targetChatID, reply); err != nil {
				b.log.Printf("feishu reply error: %v", err)
			}
		}
	}(content.Text, parentID, msgID, chatID)

	return nil
}

func (b *FeishuBot) isAuthorized(openID, userID, chatID string) bool {
	if len(b.cfg.AllowedUsers) > 0 {
		for _, allowed := range b.cfg.AllowedUsers {
			a := strings.TrimSpace(allowed)
			if a != "" && (a == openID || a == userID) {
				return true
			}
		}
		return false
	}
	switch strings.ToLower(strings.TrimSpace(b.cfg.ReceiveIDType)) {
	case "", "open_id":
		return b.cfg.ReceiveID == "" || openID == b.cfg.ReceiveID
	case "user_id":
		return b.cfg.ReceiveID == "" || userID == b.cfg.ReceiveID
	case "chat_id":
		return b.cfg.ReceiveID == "" || chatID == b.cfg.ReceiveID
	default:
		return true
	}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
