// Package qq 提供 QQ 官方机器人适配器的公开构造入口。
//
// New 创建独立 Adapter；每个账号的接入模式、凭证和事件状态由实例持有。
package qq

import (
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	implementation "github.com/AlonaBot/alonabot-adapter-qq/internal/qq"
)

// AdapterID 标识 QQ 官方机器人平台。
const AdapterID = "qq"

// component 保持工程入口身份，SDK 通过此包绑定组件资源。
type component struct{ adapter.Adapter }

// New 创建同时提供 WebSocket 与 Webhook 接入的适配器。
func New() adapter.Adapter { return &component{implementation.New()} }
