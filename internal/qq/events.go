package qq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk/message"
)

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
	opCallbackACK    = 12
	opValidation     = 13
)

type payload struct {
	ID       string          `json:"id"`
	Op       int             `json:"op"`
	Sequence *int64          `json:"s,omitempty"`
	Event    string          `json:"t"`
	Body     json.RawMessage `json:"d"`
}

type cursor struct {
	SessionID string `json:"session_id,omitempty"`
	Sequence  *int64 `json:"sequence,omitempty"`
}

type qqUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	MemberOpenID string `json:"member_openid"`
	UserOpenID   string `json:"user_openid"`
	Role         string `json:"member_role"`
}

type attachment struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	MIME     string `json:"content_type"`
	Size     uint64 `json:"size"`
}

type incomingMessage struct {
	ID          string       `json:"id"`
	Content     string       `json:"content"`
	Timestamp   time.Time    `json:"timestamp"`
	Author      qqUser       `json:"author"`
	GroupOpenID string       `json:"group_openid"`
	ChannelID   string       `json:"channel_id"`
	GuildID     string       `json:"guild_id"`
	Mentions    []qqUser     `json:"mentions"`
	Attachments []attachment `json:"attachments"`
	Reference   *struct {
		ID string `json:"message_id"`
	} `json:"message_reference"`
}

type interaction struct {
	ID                string    `json:"id"`
	ApplicationID     string    `json:"application_id"`
	Type              int       `json:"type"`
	Scene             string    `json:"scene"`
	ChatType          int       `json:"chat_type"`
	GroupOpenID       string    `json:"group_openid"`
	GroupMemberOpenID string    `json:"group_member_openid"`
	UserOpenID        string    `json:"user_openid"`
	ChannelID         string    `json:"channel_id"`
	Timestamp         time.Time `json:"timestamp"`
	Body              struct {
		Resolved struct {
			ButtonData string `json:"button_data"`
			MessageID  string `json:"message_id"`
			UserID     string `json:"user_id"`
		} `json:"resolved"`
	} `json:"data"`
}

// replyContext 持久保存 QQ 被动回复凭据和序号；过期后按主动发送处理。
type replyContext struct {
	MessageID string    `json:"message_id,omitempty"`
	EventID   string    `json:"event_id,omitempty"`
	At        time.Time `json:"at"`
	Sequence  uint32    `json:"sequence"`
	Session   string    `json:"session"`
}

// dispatch 串行准入事件；磁盘 ID 日志避免重放时重复执行插件副作用。
// 日志在 Core 接纳后落盘，进程在两个提交点之间崩溃时保持至少一次交付语义。
func (i *instance) dispatch(ctx context.Context, p payload, raw []byte) error {
	i.receiveMu.Lock()
	defer i.receiveMu.Unlock()
	if err := i.ctx.Err(); err != nil {
		return err
	}
	if p.Sequence != nil && i.config.Mode == "websocket" && i.cursor.Sequence != nil && *p.Sequence <= *i.cursor.Sequence {
		return nil
	}
	event, reply, err := i.convert(p, raw)
	if err != nil {
		return err
	}
	if event == nil {
		return i.saveSequence(ctx, p.Sequence)
	}
	key := p.ID
	if key == "" {
		switch body := event.Payload.(type) {
		case *message.CreatedPayload:
			key = p.Event + ":" + event.Session + ":" + body.PlatformMessageID
		case *message.ButtonClickedPayload:
			key = p.Event + ":" + body.CallbackQueryID
		}
		event.SourceEventID = key
	}
	if _, err := os.Stat(i.statePath("event", key)); err == nil {
		return i.saveSequence(ctx, p.Sequence)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := i.cursor
	if p.Sequence != nil {
		next.Sequence = p.Sequence
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if event != nil {
		// 先保存回复上下文，Core 在 Deliver 返回前即可调度自动回复。
		if reply != nil {
			i.sendMu.Lock()
			err = i.writeState("reply", event.Session, reply)
			if err == nil && reply.MessageID != "" {
				err = i.writeState("message", reply.MessageID, reply)
			}
			i.sendMu.Unlock()
			if err != nil {
				return err
			}
		}
		if event.Type == "button.clicked" {
			callback := event.Payload.(*message.ButtonClickedPayload)
			if err := i.client.call(ctx, http.MethodPut, "/interactions/"+pathID(callback.CallbackQueryID), map[string]int{"code": 0}, nil); err != nil {
				slog.Warn("QQ button callback acknowledgement failed", "instance_id", i.id, "error", i.client.safe(err))
			}
		}
		if _, err := i.core.Deliver(ctx, event, encoded); err != nil {
			return err
		}
	} else if err := i.core.SaveCursor(ctx, encoded); err != nil {
		return err
	}
	i.cursor = next
	return i.writeState("event", key, struct{}{})
}

func (i *instance) saveSequence(ctx context.Context, sequence *int64) error {
	if sequence == nil {
		return nil
	}
	next := i.cursor
	next.Sequence = sequence
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := i.core.SaveCursor(ctx, encoded); err != nil {
		return err
	}
	i.cursor = next
	return nil
}

func (i *instance) convert(p payload, raw []byte) (*message.Event, *replyContext, error) {
	event := &message.Event{Source: message.SourceAdapter, AdapterID: adapterID, AdapterInstanceID: i.id, AccountID: i.account.ID, SourceEventID: p.ID, PlatformPayload: raw, Capabilities: capabilities()}
	if p.Event == "INTERACTION_CREATE" {
		return i.convertInteraction(p, event)
	}
	var scene string
	switch p.Event {
	case "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE":
		scene = "group"
		event.ChatType = message.ChatGroup
	case "C2C_MESSAGE_CREATE":
		scene = "c2c"
		event.ChatType = message.ChatPrivate
	case "AT_MESSAGE_CREATE", "MESSAGE_CREATE":
		scene = "channel"
		event.ChatType = message.ChatChannel
	case "DIRECT_MESSAGE_CREATE":
		scene = "dm"
		event.ChatType = message.ChatPrivate
	default:
		return nil, nil, nil
	}
	var incoming incomingMessage
	if err := json.Unmarshal(p.Body, &incoming); err != nil {
		return nil, nil, fmt.Errorf("decode QQ %s: %w", p.Event, err)
	}
	id, user := incoming.ChannelID, incoming.Author.ID
	switch scene {
	case "group":
		id, user = incoming.GroupOpenID, incoming.Author.MemberOpenID
	case "c2c":
		id, user = incoming.Author.UserOpenID, incoming.Author.UserOpenID
	case "dm":
		id = incoming.GuildID
	}
	if id == "" || user == "" || incoming.ID == "" || incoming.Timestamp.IsZero() {
		return nil, nil, fmt.Errorf("QQ %s has no target, author, message id or timestamp", p.Event)
	}
	event.Type = "message.created"
	event.Session = targetSession(scene, id)
	event.OccurredAt = incoming.Timestamp
	event.Actor = &message.Actor{ExternalUserID: user, AdapterInstanceID: i.id, DisplayName: incoming.Author.Username}
	text := incoming.Content
	toMe := scene == "c2c" || scene == "dm" || p.Event == "GROUP_AT_MESSAGE_CREATE" || p.Event == "AT_MESSAGE_CREATE"
	for _, mention := range incoming.Mentions {
		if mention.ID == i.account.ID {
			toMe = true
		}
	}
	// QQ 的 @事件可能仍含机器人前缀，只移除这个确定指向本机器人的前缀。
	if toMe {
		for _, prefix := range []string{"<@!" + i.account.ID + ">", "<@" + i.account.ID + ">"} {
			if strings.HasPrefix(text, prefix) {
				text = strings.TrimPrefix(text, prefix)
				text = strings.TrimLeft(text, " ")
				break
			}
		}
	}
	chain := parseMentions(text)
	if incoming.Reference != nil && incoming.Reference.ID != "" {
		chain = append(chain, message.Quote(message.Ref(event.Session, incoming.Reference.ID, i.id)))
	}
	for _, file := range incoming.Attachments {
		source, err := i.incomingSource(file)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case strings.HasPrefix(file.MIME, "image/"):
			chain = append(chain, message.Image(source))
		case strings.HasPrefix(file.MIME, "video/"):
			chain = append(chain, message.Video(source))
		case file.MIME == "voice" || strings.HasPrefix(file.MIME, "audio/"):
			chain = append(chain, message.Voice(source))
		default:
			chain = append(chain, message.File(source, file.Filename))
		}
	}
	event.Message = &message.PlatformMessage{PlatformMessageID: incoming.ID, Chain: chain}
	event.Payload = &message.CreatedPayload{PlatformMessageID: incoming.ID, Chain: chain, ToMe: toMe}
	return event, &replyContext{Session: event.Session, MessageID: incoming.ID, At: incoming.Timestamp}, nil
}

func (i *instance) convertInteraction(p payload, event *message.Event) (*message.Event, *replyContext, error) {
	var click interaction
	if err := json.Unmarshal(p.Body, &click); err != nil {
		return nil, nil, fmt.Errorf("decode QQ interaction: %w", err)
	}
	if click.Type != 11 {
		return nil, nil, nil
	}
	if click.ApplicationID != "" && click.ApplicationID != i.config.AppID {
		return nil, nil, errors.New("QQ interaction application does not match instance")
	}
	scene, id, user := click.Scene, click.UserOpenID, click.UserOpenID
	if scene == "" {
		switch click.ChatType {
		case 0:
			scene = "guild"
		case 1:
			scene = "group"
		case 2:
			scene = "c2c"
		}
	}
	switch scene {
	case "group":
		id, user = click.GroupOpenID, click.GroupMemberOpenID
		event.ChatType = message.ChatGroup
	case "c2c":
		event.ChatType = message.ChatPrivate
	case "guild":
		scene = "channel"
		id, user = click.ChannelID, click.Body.Resolved.UserID
		event.ChatType = message.ChatChannel
	default:
		return nil, nil, errors.New("QQ button interaction has an unknown scene")
	}
	if id == "" || user == "" || click.ID == "" || click.Body.Resolved.ButtonData == "" {
		return nil, nil, errors.New("QQ button interaction has no target, actor, id or token")
	}
	// 频道私信复用 guild 互动载荷，原消息绑定恢复实际会话。
	if click.Scene == "guild" && click.Body.Resolved.MessageID != "" {
		var sentSession string
		i.sendMu.Lock() // 平台可能在发送回执返回后立即推送点击，等待绑定落盘。
		bindingErr := i.readState("keyboard", click.Body.Resolved.MessageID, &sentSession)
		i.sendMu.Unlock()
		if err := bindingErr; err == nil {
			sentScene, sentID, err := parseTarget(sentSession)
			if err != nil {
				return nil, nil, err
			}
			if sentScene == "dm" {
				scene, id = sentScene, sentID
				event.ChatType = message.ChatPrivate
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
	}
	event.Type = "button.clicked"
	event.Session = targetSession(scene, id)
	event.Actor = &message.Actor{ExternalUserID: user, AdapterInstanceID: i.id}
	event.OccurredAt = click.Timestamp
	event.Payload = &message.ButtonClickedPayload{Value: click.Body.Resolved.ButtonData, CallbackQueryID: click.ID, OriginPlatformMessageID: click.Body.Resolved.MessageID}
	eventID := p.ID
	if eventID == "" {
		eventID = "INTERACTION_CREATE:" + click.ID
	}
	return event, &replyContext{Session: event.Session, EventID: eventID, At: click.Timestamp}, nil
}

func parseMentions(text string) message.Chain {
	var chain message.Chain
	for text != "" {
		start := strings.Index(text, "<@")
		if start < 0 {
			chain = append(chain, message.Text(text))
			break
		}
		end := strings.IndexByte(text[start:], '>')
		if end < 0 {
			chain = append(chain, message.Text(text))
			break
		}
		if start > 0 {
			chain = append(chain, message.Text(text[:start]))
		}
		id := strings.TrimPrefix(text[start+2:start+end], "!")
		if id == "" {
			chain = append(chain, message.Text(text[start:start+end+1]))
		} else {
			chain = append(chain, message.Mention(id))
		}
		text = text[start+end+1:]
	}
	return chain
}
