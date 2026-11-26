# AlonaBot QQ adapter

[English](README.md) · [简体中文](README.zh-CN.md)

Connect AlonaBot to QQ's official Bot API: groups, C2C chats, channels and channel DMs, using Webhook or WebSocket. Supports messages, media, Markdown and callback/URL buttons through the existing AlonaBot SDK.

## Install and configure

```fish
alonabot install --source AlonaBot/alonabot-adapter-qq
```

Installation requires a published release. To build from source, follow [development](docs/development.md). Configure an instance in `core_config.json` and enable the required events/permissions in QQ's developer console. See [usage](docs/usage.md) for the complete configuration, callback URL and scenario capability matrix.

## Develop

This ordinary component repository was generated from [FlanChanXwO/alonabot-plugin-template](https://github.com/FlanChanXwO/alonabot-plugin-template). `qq.go` exports `New() adapter.Adapter`; `internal/qq/` owns the implementation; the command calls `sdk.RunAdapter`.

- [Usage and buttons](docs/usage.md)
- [Development and verification](docs/development.md)
- [Packaging and releases](docs/releasing.md)
- [Contributing](CONTRIBUTING.md) · [Coding standards](docs/coding-standards.md)

[MIT License](LICENSE)
