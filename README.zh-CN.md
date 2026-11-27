# AlonaBot QQ 适配器

<img src="assets/qq.png" width="62" height="62" alt="QQ">

[English](README.md) · [简体中文](README.zh-CN.md)

通过 QQ 官方 Bot API 接入 AlonaBot，覆盖 QQ 群、C2C 单聊、频道与频道私信，提供 Webhook 和 WebSocket 两种接入。消息、媒体、Markdown 及回调/URL 按钮沿用 AlonaBot SDK。

## 安装与配置

```fish
alonabot install --source AlonaBot/alonabot-adapter-qq
```

安装需要已发布的 Release。源码构建见[开发指南](docs/development.zh-CN.md)。在 `core_config.json` 配置账号实例，并在 QQ 开放平台启用所需事件和权限。[使用指南](docs/usage.zh-CN.md)提供完整配置、回调地址与场景能力矩阵。

## 开发

本普通组件仓库派生自 [FlanChanXwO/alonabot-plugin-template](https://github.com/FlanChanXwO/alonabot-plugin-template)。`qq.go` 导出 `New() adapter.Adapter`，`internal/qq/` 负责实现，命令入口调用 `sdk.RunAdapter`。

- [使用与按钮](docs/usage.zh-CN.md)
- [开发与验证](docs/development.zh-CN.md)
- [打包与发布](docs/releasing.zh-CN.md)
- [贡献指南](CONTRIBUTING.zh-CN.md) · [编码规范](docs/coding-standards.zh-CN.md)

[MIT License](LICENSE)

QQ Logo 版权归腾讯所有，素材原样取自 [QQ 官网](https://im.qq.com/index)的[官方 PNG](https://static-res.qq.com/static-res/imqq/qq-logo.png)。
