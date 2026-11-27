package qq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	"github.com/coder/websocket"
)

const (
	// QQ 协议要求连接故障后重新获取网关；间隔避免断网时的忙循环。
	reconnectInterval   = time.Second
	groupAndC2CIntent   = 1 << 25
	interactionIntent   = 1 << 26
	directMessageIntent = 1 << 12
	guildMentionIntent  = 1 << 30
	guildMessageIntent  = 1 << 9
)

type gateway struct {
	conn         *websocket.Conn
	interval     time.Duration
	sequence     atomic.Int64 // 接收协程推进，心跳协程读取。
	hasSequence  atomic.Bool
	acknowledged atomic.Bool
}

func (g *gateway) close() { _ = g.conn.CloseNow() }

func (i *instance) connect(ctx context.Context) (*gateway, error) {
	// 1. 每次连接读取平台网关并验证 Hello 提供的心跳周期。
	var address struct {
		URL string `json:"url"`
	}
	if err := i.client.call(ctx, http.MethodGet, "/gateway", nil, &address); err != nil {
		return nil, err
	}
	conn, response, err := websocket.Dial(ctx, address.URL, &websocket.DialOptions{HTTPClient: i.client.http})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("connect QQ gateway: %w", err)
	}
	// 官方事件体可能含多附件，使用平台/宿主预算，移除库的默认 32 KiB 限制。
	conn.SetReadLimit(-1)
	g := &gateway{conn: conn}
	failed := true
	defer func() {
		if failed {
			g.close()
		}
	}()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var hello payload
	if err := json.Unmarshal(raw, &hello); err != nil {
		return nil, fmt.Errorf("decode QQ Hello: %w", err)
	}
	var timing struct {
		Milliseconds int64 `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(hello.Body, &timing); err != nil || hello.Op != opHello || timing.Milliseconds <= 0 {
		return nil, errors.New("invalid QQ gateway Hello")
	}
	g.interval = time.Duration(timing.Milliseconds) * time.Millisecond
	token, err := i.client.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	// 2. 已保存 Session 时恢复；失效时由接收循环清除 Session 后重新鉴权。
	i.receiveMu.Lock()
	previous := i.cursor
	i.receiveMu.Unlock()
	auth := map[string]any{"token": "QQBot " + token}
	opcode := opIdentify
	if previous.SessionID != "" && previous.Sequence != nil {
		opcode = opResume
		auth["session_id"], auth["seq"] = previous.SessionID, *previous.Sequence
		g.sequence.Store(*previous.Sequence)
		g.hasSequence.Store(true)
	} else {
		intents := groupAndC2CIntent | interactionIntent | directMessageIntent
		if i.config.GuildMessages == "all" {
			intents |= guildMessageIntent
		} else {
			intents |= guildMentionIntent
		}
		auth["intents"], auth["shard"] = intents, []int{0, 1}
	}
	if err := g.write(ctx, opcode, auth); err != nil {
		return nil, err
	}
	failed = false
	return g, nil
}

func (g *gateway) write(ctx context.Context, op int, body any) error {
	encoded, err := json.Marshal(struct {
		Op   int `json:"op"`
		Body any `json:"d"`
	}{op, body})
	if err != nil {
		return err
	}
	return g.conn.Write(ctx, websocket.MessageText, encoded)
}

func (g *gateway) heartbeat(ctx context.Context) error {
	var sequence any
	if g.hasSequence.Load() {
		sequence = g.sequence.Load()
	}
	return g.write(ctx, opHeartbeat, sequence)
}

func (i *instance) runGateway(g *gateway) {
	defer close(i.done) // Stop 等待此通道，确保再无事件交付。
	for {
		err := i.receiveGateway(g)
		g.close()
		if i.ctx.Err() != nil {
			return
		}
		status := websocket.CloseStatus(err)
		if status == 4001 || status == 4002 || status == 4004 || (status >= 4010 && status <= 4014) || status == 4914 || status == 4915 {
			i.fail(fmt.Errorf("QQ gateway rejected authentication or intents: %w", err))
			return
		}
		if status == 4006 || status == 4007 || (status >= 4900 && status <= 4913) {
			i.receiveMu.Lock()
			i.cursor = cursor{}
			cursorErr := i.core.SaveCursor(i.ctx, []byte(`{}`))
			i.receiveMu.Unlock()
			if cursorErr != nil {
				i.fail(cursorErr)
				return
			}
		}
		slog.Warn("QQ gateway disconnected; reconnecting", "instance_id", i.id, "error", i.client.safe(err))
		for {
			timer := time.NewTimer(reconnectInterval)
			select { // 连接恢复属于平台协议，停止实例时立即打断等待。
			case <-i.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			var connectErr error
			g, connectErr = i.connect(i.ctx)
			if connectErr == nil {
				break
			}
			var rejected *apiError
			if errors.As(connectErr, &rejected) && rejected.status >= 400 && rejected.status < 500 {
				i.fail(connectErr)
				return
			}
			slog.Warn("QQ gateway reconnect failed", "instance_id", i.id, "error", i.client.safe(connectErr))
		}
	}
}

func (i *instance) receiveGateway(g *gateway) error {
	ctx, cancel := context.WithCancel(i.ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	g.acknowledged.Store(true)
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()
		for {
			select { // 心跳独立于 Core 事件接纳，避免插件处理影响连接保活。
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !g.acknowledged.Swap(false) {
					g.close()
					return
				}
				if err := g.heartbeat(ctx); err != nil {
					g.close()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	for {
		_, raw, err := g.conn.Read(ctx)
		if err != nil {
			return err
		}
		var p payload
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("decode QQ gateway event: %w", err)
		}
		switch p.Op {
		case opHeartbeatACK:
			g.acknowledged.Store(true)
		case opHeartbeat:
			if err := g.heartbeat(ctx); err != nil {
				return err
			}
		case opReconnect:
			return errors.New("QQ gateway requested reconnect")
		case opInvalidSession:
			var resumable bool
			if err := json.Unmarshal(p.Body, &resumable); err != nil {
				return err
			}
			if !resumable {
				i.receiveMu.Lock()
				i.cursor = cursor{}
				i.receiveMu.Unlock()
				if err := i.core.SaveCursor(ctx, []byte(`{}`)); err != nil {
					return err
				}
			}
			return errors.New("QQ gateway session is invalid")
		case opDispatch:
			if p.Event == "READY" {
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(p.Body, &ready); err != nil || ready.SessionID == "" {
					return errors.New("invalid QQ READY event")
				}
				i.receiveMu.Lock()
				i.cursor = cursor{SessionID: ready.SessionID}
				err = i.saveSequence(ctx, p.Sequence)
				i.receiveMu.Unlock()
			} else if p.Event == "RESUMED" {
				i.receiveMu.Lock()
				err = i.saveSequence(ctx, p.Sequence)
				i.receiveMu.Unlock()
			} else {
				err = i.dispatch(ctx, p, raw)
			}
			if err != nil {
				i.fail(err)
				return err
			}
			if p.Sequence != nil {
				g.sequence.Store(*p.Sequence)
				g.hasSequence.Store(true)
			}
		}
	}
}

func (i *instance) fail(err error) {
	safe := i.client.safe(err)
	if reportErr := i.core.ReportFailure(i.ctx, errcode.Unavailable.New(safe.Error())); reportErr != nil {
		slog.Error("QQ instance failure report failed", "instance_id", i.id, "error", reportErr)
	}
	i.cancel()
}
