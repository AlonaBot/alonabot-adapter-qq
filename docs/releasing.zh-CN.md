# 打包与发布 QQ 适配器

[English](releasing.md)

完成[开发验证](development.zh-CN.md)后生成本地安装包：

```fish
alonabot pack . --target linux/amd64 --out bin
```

模板发布流程支持 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64，归档包含组件声明、可执行文件、资源与 `LICENSE`。

## 准备发行

更新 `plugin.yaml` 版本和 `changelog/<版本>/{en-US,zh-CN}.md`。PR 审查通过并由维护者合并后，取得发布授权，再推送匹配版本的 `v*` 标签。

[Release 工作流](../.github/workflows/release.yml)解析 SDK `latest`、构建对应 CLI、运行测试和组件检查，然后执行：

```fish
bash scripts/release.sh v0.1.0 AlonaBot/alonabot-adapter-qq
```

脚本检查版本、source、双语 changelog 和归档中的 `LICENSE`，打包五平台并生成双语发布说明。工作流将归档发布为 GitHub Release。本次实现交付到 PR；标签、合并和发行由维护者执行。

版本说明应区分模拟平台验证与真实 QQ 联调，保留账号权限、频道能力及 WebSocket 可用性的实际限制。
