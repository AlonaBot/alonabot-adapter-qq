// Package qq 实现 QQ 官方 API 的账号、事件接入与消息发送。
//
// 一个 Adapter 持有账号索引，实例拥有令牌、连接及磁盘状态。
// 接收串行提交事件和游标；发送串行分配 QQ msg_seq 并保留各物理操作回执。
package qq

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AlonaBot/AlonaBot/sdk"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	"github.com/AlonaBot/AlonaBot/sdk/message"
)

const adapterID = "qq"

// Adapter 管理由 SDK 启停的账号索引；配置只保留在所属实例。
type Adapter struct {
	mu        sync.RWMutex
	instances map[string]*instance
}

type configuration struct {
	AppID         string `json:"app_id"`
	AppSecret     string `json:"app_secret"`
	Mode          string `json:"mode"`
	Sandbox       bool   `json:"sandbox"`
	GuildMessages string `json:"guild_messages"`
}

// instance 的 opMu 保护操作计数和排空通道；receiveMu 串行化事件提交和状态写入。
type instance struct {
	owner     *Adapter
	id        string
	config    configuration
	client    *client
	core      adapter.Core
	account   adapter.Account
	directory string
	key       ed25519.PrivateKey
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	opMu      sync.Mutex
	inflight  int           // 由 opMu 保护。
	drained   chan struct{} // 首次 Stop 创建，最后一个操作结束时关闭。
	receiveMu sync.Mutex
	sendMu    sync.Mutex
	cursor    cursor
}

// New 创建独立的 QQ Adapter。
func New() *Adapter { return &Adapter{instances: make(map[string]*instance)} }

// Descriptor 登记由 SDK 托管且由 QQ 签名认证的公开回调入口。
func (a *Adapter) Descriptor() *adapter.Descriptor {
	return &adapter.Descriptor{AdapterID: adapterID, Capabilities: capabilities(), HTTP: []adapter.HTTPRoute{
		{Method: http.MethodPost, Path: "/webhook/", Auth: adapter.AuthPlatformToken, Handler: http.HandlerFunc(a.webhook)},
	}}
}

func capabilities() []message.Capability {
	names := []string{"message.send", "message.markdown", "message.reference", "message.buttons", "message.voice", "media.read"}
	out := make([]message.Capability, 0, len(names))
	for _, name := range names {
		out = append(out, message.Capability{Name: name, Version: 1, Supported: true})
	}
	return out
}

// Start 校验凭证、验证账号并建立所选接收链路。
func (a *Adapter) Start(ctx context.Context, req adapter.StartRequest, core adapter.Core) (adapter.Instance, error) {
	// 1. 配置错误只报告字段与结构原因，避免回显凭据。
	config := configuration{Mode: "webhook", GuildMessages: "mentions"}
	if err := json.Unmarshal(req.ConfigJSON, &config); err != nil {
		return nil, errors.New("invalid QQ instance configuration JSON")
	}
	if config.AppID == "" || config.AppSecret == "" {
		return nil, errors.New("app_id and app_secret are required")
	}
	if config.Mode != "websocket" && config.Mode != "webhook" {
		return nil, errors.New("mode must be webhook or websocket")
	}
	if config.GuildMessages != "mentions" && config.GuildMessages != "all" {
		return nil, errors.New("guild_messages must be mentions or all")
	}
	baseURL := apiURL
	if config.Sandbox {
		baseURL = sandboxURL
	}
	c := &client{http: &http.Client{Transport: http.DefaultTransport}, appID: config.AppID, secret: config.AppSecret, baseURL: baseURL}
	var account struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := c.call(ctx, http.MethodGet, "/users/@me", nil, &account); err != nil {
		return nil, c.safe(err)
	}
	if account.ID == "" {
		return nil, errors.New("QQ account response has no id")
	}
	// 2. 实例生命期独立于启动 RPC；Stop 取消连接和已进入的操作。
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	i := &instance{owner: a, id: req.InstanceID, config: config, client: c, core: core, account: adapter.Account{ID: account.ID, DisplayName: account.Username}, ctx: lifetime, cancel: cancel, done: make(chan struct{}), key: signingKey(config.AppSecret)}
	i.directory = filepath.Join(sdk.PluginDataDir(ctx), "qq", fileKey(req.InstanceID), fileKey(config.AppID))
	if err := os.MkdirAll(i.directory, 0700); err != nil {
		cancel()
		return nil, fmt.Errorf("create QQ instance state: %w", err)
	}
	if len(req.Cursor) > 0 {
		if err := json.Unmarshal(req.Cursor, &i.cursor); err != nil {
			cancel()
			return nil, fmt.Errorf("decode QQ cursor: %w", err)
		}
	}
	var connection *gateway
	if config.Mode == "websocket" {
		var err error
		connection, err = i.connect(ctx)
		if err != nil {
			cancel()
			return nil, c.safe(err)
		}
	}
	// 3. 注册后才对公开路由可见，避免访问半初始化实例。
	a.mu.Lock()
	if _, exists := a.instances[i.id]; exists {
		a.mu.Unlock()
		cancel()
		if connection != nil {
			connection.close()
		}
		return nil, errors.New("QQ instance already exists")
	}
	a.instances[i.id] = i
	a.mu.Unlock()
	if connection != nil {
		go i.runGateway(connection)
	} else {
		close(i.done)
	}
	return i, nil
}

// Account 返回由 QQ API 验证的机器人身份。
func (i *instance) Account() adapter.Account { return i.account }

// Capabilities 返回实例已实现的通用能力；发送时再检查具体场景。
func (i *instance) Capabilities() []message.Capability { return capabilities() }

// Stop 先取消网络操作，再等待接收和已进入的请求结束。
func (i *instance) Stop(ctx context.Context) error {
	i.cancel()
	i.owner.mu.Lock()
	if i.owner.instances[i.id] == i {
		delete(i.owner.instances, i.id)
	}
	i.owner.mu.Unlock()
	select { // 等待接收协程退出，同时遵守宿主停止期限。
	case <-i.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	i.opMu.Lock()
	if i.drained == nil {
		i.drained = make(chan struct{})
		if i.inflight == 0 {
			close(i.drained)
		}
	}
	drained := i.drained
	i.opMu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *instance) operation(ctx context.Context) (context.Context, func(), error) {
	i.opMu.Lock()
	if err := i.ctx.Err(); err != nil {
		i.opMu.Unlock()
		return nil, nil, errcode.FailedPrecondition.New("QQ instance is stopped")
	}
	i.inflight++
	i.opMu.Unlock()
	linked, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(i.ctx, cancel)
	return linked, func() {
		stop()
		cancel()
		i.opMu.Lock()
		i.inflight--
		if i.inflight == 0 && i.drained != nil {
			close(i.drained)
		}
		i.opMu.Unlock()
	}, nil
}

func signingKey(secret string) ed25519.PrivateKey {
	seed := secret
	for len(seed) < ed25519.SeedSize {
		seed += seed
	}
	return ed25519.NewKeyFromSeed([]byte(seed[:ed25519.SeedSize]))
}

func fileKey(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func (i *instance) statePath(prefix, key string) string {
	return filepath.Join(i.directory, prefix+"-"+fileKey(key)+".json")
}

// writeState 原子替换实例状态，崩溃时保留上一份完整内容。
func (i *instance) writeState(prefix, key string, value any) (err error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(i.directory, ".state-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), i.statePath(prefix, key))
}

func (i *instance) readState(prefix, key string, out any) error {
	encoded, err := os.ReadFile(i.statePath(prefix, key))
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func targetSession(scene, id string) string { return message.SID(adapterID, scene+":"+id, "") }

func parseTarget(to string) (scene, id string, err error) {
	platform, chat, thread, err := message.ParseSID(to)
	if err != nil {
		return "", "", err
	}
	if platform != adapterID || thread != "" {
		return "", "", errors.New("invalid QQ target")
	}
	scene, id, ok := strings.Cut(chat, ":")
	if !ok || id == "" {
		return "", "", errors.New("QQ target requires a scene and platform id")
	}
	switch scene {
	case "group", "c2c", "channel", "dm":
		return scene, id, nil
	}
	return "", "", errors.New("unknown QQ target scene")
}
