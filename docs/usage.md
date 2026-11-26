# Connect an official QQ bot

[简体中文](usage.zh-CN.md)

Create a bot in QQ's developer console, obtain its AppID/AppSecret, and configure sandbox members, API permissions and outbound IP allowlisting. QQ controls approval, Markdown/buttons, proactive messaging and group message visibility.

## Configure an account

Merge this into the Core data directory's `core_config.json`:

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

`app_secret` is a secret field and supports Core environment references. Keep credentials local. See [adapter_config_schema.json](../adapter_config_schema.json).

- `mode`: `webhook` (default) or `websocket`, one transport per instance.
- `sandbox`: defaults to `false`; enables the QQ sandbox API.
- `guild_messages`: `mentions` (default) subscribes to channel mentions; `all` subscribes to all channel messages and requires QQ permission.

Restart or re-enable the instance through Core management after editing. One Adapter process can manage multiple isolated accounts.

## Webhook

Reverse-proxy public HTTPS to Core and configure this QQ callback URL:

```text
https://YOUR_DOMAIN/plugins/alonabot-adapter-qq/webhook/main
```

`main` matches the instance name. The Adapter verifies the QQ signature with that instance's AppSecret; a Core management token is unnecessary. Subscribe to group, C2C, channel, channel DM events and `INTERACTION_CREATE`. Full group messages also require `GROUP_MESSAGE_CREATE` and permission in the QQ group settings.

Your deployment supplies the HTTPS domain and certificate. Start the Core instance before saving QQ's callback settings so verification can complete.

## WebSocket

Set `mode` to `websocket`. The Adapter obtains the gateway, authenticates, follows QQ's heartbeat interval, saves its Session/processed sequence and resumes after disconnects. No inbound callback is needed. Confirm account-specific WebSocket availability during live testing: older SDK deprecation notices and current API documentation differ in age.

## Sessions and content

| Content | QQ group / C2C | Channel / channel DM |
| --- | --- | --- |
| Text, mentions, references | Receive/send | Receive/send |
| Markdown | Send, permission required | Send, permission required |
| Images | Receive/send | Receive/send |
| Voice, video, files | Receive/send | Receive attachments provided by QQ; sending returns `UNSUPPORTED` |
| Callback and URL buttons | Send with Markdown | Send with Markdown; DM keyboard follows QQ's parameter inheritance and awaits live confirmation |

Session strings are `qq:group:<group_openid>`, `qq:c2c:<user_openid>`, `qq:channel:<channel_id>` and `qq:dm:<dm_guild_id>`. Plugins normally use the event's default session. Treat QQ OpenIDs as scoped to their application and scene.

Text/media retain their original order; adjacent text in the same format is merged. Buttons attach to the final Markdown message. Plain text with buttons is escaped into QQ Markdown. Final media content with buttons returns `UNSUPPORTED` before contacting QQ. A logical send may contain several physical sends; failures preserve confirmed IDs and partial results. Platform rejection, quotas and unknown outcomes are returned without automatic send retries.

Without an explicit reference, sends use the session's latest incoming message/button context for passive replies. After QQ's five-minute reply window, sends are proactive. Explicit incoming-message references use their own context. Each send durably allocates `msg_seq`; proactive sends require QQ authorization and quota.

## Media

Group/C2C uploads precede sending `file_info`. HTTPS sources go directly to QQ. Core media tokens use temporary `Core.MediaURL` URLs: set Core `base_url` to public HTTPS reachable by QQ and proxy `/media/`. Channel and channel DM images stream Core media through multipart. Inline Base64 up to 5 MiB uses direct `file_data` uploads in group/C2C; upload larger content to Core with `sdk/media.Upload` first.

No transcoding is performed. Prefer SILK for group/C2C voice; other formats depend on platform acceptance. Incoming attachments are read in chunks on demand. Signed file handles become invalid after credential rotation; expired platform download URLs return errors.

## Buttons in plugins

```go
// Selection carries the button's application parameters.
type Selection struct {
    Item string `json:"item"`
}

// Register in the plugin constructor; p is *sdk.Plugin.
action := button.Define[Selection]("select")
button.On(p, action).Handle(func(c *sdk.Context, selected Selection) error {
    // Handle selected.Item here; return errors to Core.
    return nil
})

// In a message Handler, localize the prompt and labels through plugin i18n.
_, err := c.Send(message.Chain{
    message.Markdown(c.T("selection_prompt")),
    message.Buttons([]message.Button{
        action.Button(c.T("selection_confirm"), Selection{Item: "example"}),
        message.URLButton(c.T("selection_docs"), "https://alonabot.github.io/"),
    }),
})
```

Import `sdk`, `sdk/button` and `sdk/message`. A keyboard supports five rows, five buttons per row and ten Unicode characters per label; excess input returns an error without truncation. Core validates tokens, schemas, targets and each click's permissions. The Adapter renders QQ buttons, preserves the clicking user's identity and acknowledges interactions. QQ acknowledgement ends client waiting; the plugin sends the application result separately.

## Receive state and validation

Deduplication records, reply contexts and message sequences reside in the managed component data directory and survive restarts. The event journal is written after Core admission; a crash between these commits can cause another delivery. QQ publishes no definite replay window, so this version retains records until component data cleanup; disk usage grows with event volume.

Automated verification uses local QQ simulators and real Core/SDK. Live QQ accounts, permissions, moderation, channel DM keyboards and WebSocket availability require later platform testing.

Official references: [signatures](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/sign.html), [group messages](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html), [channel messages](https://bot.q.qq.com/wiki/develop/api-v2/server-inter/channel/message/send.html), [interaction acknowledgement](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/interactions_interaction_id.put.html).
