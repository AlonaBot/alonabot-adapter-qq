# 接入 QQ 官方机器人

[English](usage.md)

在 QQ 开放平台创建机器人，取得 AppID、AppSecret，配置沙箱成员、所需接口权限和出口 IP 白名单。账号审核、按钮与 Markdown 权限、群主动发言和消息范围由 QQ 平台控制。

## 配置账号

在 Core 数据目录的 `core_config.json` 中合并：

```json
{
  "adapters": {
    "qq": {
      "default_instance": "main",
      "instances": {
        "main": {
          "app_id": "YOUR_APP_ID",
          "app_secret": "${QQ_APP_SECRET}",
          "mode": "webhook",
          "sandbox": false,
          "guild_messages": "mentions"
        }
      }
    }
  }
}
```

`app_secret` 是 secret 字段，支持 Core 的环境变量引用。凭证仅配置在本机。配置声明见 [adapter_config_schema.json](../adapter_config_schema.json)。

- `mode`：`webhook`（默认）或 `websocket`，每个实例选择一种。
- `sandbox`：默认 `false`，启用后使用 QQ 沙箱 API。
- `guild_messages`：`mentions`（默认）接收频道 @消息；`all` 订阅频道全量消息，需要对应平台权限。

修改后重启或通过 Core 管理入口重新启用实例。一个 Adapter 进程可管理多个独立实例。

## Webhook

将公网 HTTPS 反向代理转发到 Core，回调地址填写：

```text
https://YOUR_DOMAIN/plugins/alonabot-adapter-qq/webhook/main
```

末尾 `main` 与实例名一致。回调由适配器按该实例 AppSecret 验签，无需 Core 管理 token。在 QQ 控制台勾选群消息、C2C、频道、频道私信及 `INTERACTION_CREATE`；接收群全量消息需勾选 `GROUP_MESSAGE_CREATE` 并取得群设置授权。

公网 HTTPS、域名和证书由部署环境提供。先启动 Core 实例，再保存 QQ 回调配置，以便完成地址验证。

## WebSocket

将 `mode` 改为 `websocket`。适配器获取网关、鉴权并按 QQ 提供的周期发送心跳，保存 Session 与已处理序号，断线后按协议恢复。此模式无需入站回调地址。账号是否仍允许使用 WebSocket，需由真实账号联调确认；SDK 的旧下线说明与现行平台文档存在时间差异。

## 会话与内容

| 内容 | QQ 群 / C2C | 频道 / 频道私信 |
| --- | --- | --- |
| 文本、提及、引用 | 收发 | 收发 |
| Markdown | 发送，需权限 | 发送，需权限 |
| 图片 | 收发 | 收发 |
| 语音、视频、文件 | 收发 | 接收平台下发的附件；发送返回 `UNSUPPORTED` |
| 回调与 URL 按钮 | 随 Markdown 发送 | 随 Markdown 发送；私信 keyboard 依官方参数继承条款实现，待真实账号确认 |

会话字符串采用 `qq:group:<group_openid>`、`qq:c2c:<user_openid>`、`qq:channel:<channel_id>`、`qq:dm:<dm_guild_id>`；插件通常直接使用事件的默认会话。QQ OpenID 仅在其平台场景与应用范围内使用。

文本与媒体按原顺序发送，连续同格式文本合并。按钮附在最后一条 Markdown 上；普通文本随按钮转换为 QQ Markdown；最后一条为媒体时，在调用 QQ 前返回 `UNSUPPORTED`。一条逻辑消息可包含多个物理发送；后续失败保留已确认消息 ID 与部分成功回执。平台拒绝、限频和结果未知直接返回，发送不自动重试。

未显式引用时，实例使用该会话最近一次收到的消息/按钮事件进行被动回复；超过 QQ 的五分钟回复窗口后使用主动发送。显式引用的入站消息使用它自己的回复上下文。每次发送持久分配 `msg_seq`；主动发送受账号授权和 QQ 配额约束。

## 媒体

群/C2C 先上传媒体再发送 `file_info`。HTTPS 来源直接交给 QQ；Core 媒体令牌通过 `Core.MediaURL` 签发临时 URL，需将 Core `base_url` 配置为 QQ 可访问的公网 HTTPS 地址，并代理 `/media/`。频道及频道私信图片通过 multipart 流式读取 Core 媒体。5 MiB 以内的内联 Base64 在群/C2C 使用 `file_data` 直接上传；更大内容先经 `sdk/media.Upload` 上传到 Core。

适配器不转码。群/C2C 语音优先提供 SILK；其他格式以平台权限和响应为准。入站附件按需分块读取，签名文件句柄随账号凭证轮换失效，平台下载 URL 到期后读取会返回错误。

## 在插件中使用按钮

```go
// Selection 保存按钮动作的业务参数。
type Selection struct {
    Item string `json:"item"`
}

// 在插件构造函数中登记；p 为 *sdk.Plugin。
action := button.Define[Selection]("select")
button.On(p, action).Handle(func(c *sdk.Context, selected Selection) error {
    // 在这里处理 selected.Item；错误返回给 Core。
    return nil
})

// 在消息 Handler 中发送；正文和按钮标签由插件 i18n 提供。
_, err := c.Send(message.Chain{
    message.Markdown(c.T("selection_prompt")),
    message.Buttons([]message.Button{
        action.Button(c.T("selection_confirm"), Selection{Item: "example"}),
        message.URLButton(c.T("selection_docs"), "https://alonabot.github.io/"),
    }),
})
```

导入 `sdk`、`sdk/button`、`sdk/message`。按钮最多五行，每行五个，标签最多十个 Unicode 字符；超限返回错误并保留原输入。Core 负责 token、Schema、目标和每次点击的权限验证；适配器负责 QQ 渲染、点击者身份和互动确认。QQ 协议确认只结束客户端等待，业务结果由插件后续回复提供。

## 接收状态与验证范围

事件 ID 去重记录、回复上下文和消息序号存放在组件受管数据目录，跨重启保留。去重日志写入发生在 Core 接纳后；进程在两个提交点之间崩溃时，事件可能再次交付。QQ 未公开确定的重放窗口，本版本保留记录至组件数据清理，磁盘占用随事件量增长。

自动化验收使用本地 QQ 模拟端与真实 Core/SDK。真实 QQ 账号、权限、审核、频道私信按钮及 WebSocket 可用性留待平台联调。

官方依据：[事件与签名](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/sign.html)、[群消息发送](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html)、[频道消息发送](https://bot.q.qq.com/wiki/develop/api-v2/server-inter/channel/message/send.html)、[互动回应](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/interactions_interaction_id.put.html)。
