# 开发 QQ 适配器

[English](development.md)

在仓库根目录执行。使用 `go.mod` 声明的 Go 版本，本地运行时由 asdf 管理。安装匹配当前 SDK 的 CLI，再运行测试：

```fish
go mod download
env GOTOOLCHAIN=(go env GOVERSION) go -C (go list -m -f '{{.Dir}}' github.com/AlonaBot/AlonaBot) install ./cmd/alonabot
go build ./...
go vet ./...
go test -race -count=1 ./...
alonabot check .
alonabot test .
```

将 Go bin 目录加入 PATH。CI 与发布沿用模板的 `go get github.com/AlonaBot/AlonaBot@latest` 策略，再构建匹配 CLI；本地同步升级后审阅 module 文件。

## 代码入口

- `qq.go`：公开 `New() adapter.Adapter`，保持组件资源入口身份。
- `internal/qq/`：账号和生命周期、HTTP 客户端、Webhook/WebSocket、事件、媒体与发送计划。
- `cmd/alonabot-adapter-qq/`：调用 `sdk.RunAdapter`。
- `adapter_config_schema.json`、`plugin.yaml`：账号配置与组件身份；SDK 构建 overlay 嵌入这些源资源。
- `adapter_test.go`：运行时生成隔离的库模式应用与探测插件，通过真实 Core/SDK、HTTP 代理和本地 QQ 模拟端验证两种接入、四类会话及按钮闭环。

集成测试需要匹配 SDK 的 `alonabot` CLI，并使用临时数据目录，不访问真实 QQ。`*_local_test.go` 保留临时单元和故障验证，已被忽略；提交只保留跨组件集成测试。PR 写明提交的测试新增行数和本地测试结果。

## 联调和交叉构建

按[使用指南](usage.zh-CN.md)配置测试账号，再运行：

```fish
alonabot run . --data-dir ./data
```

此命令开始真实平台连接，需使用自己的本机凭据。普通自动化测试与 `alonabot test .` 保持离线。

```fish
for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64
    set -l parts (string split / $target)
    env GOOS=$parts[1] GOARCH=$parts[2] CGO_ENABLED=0 go build ./...
end
```

交叉编译只验证构建；平台行为留待真实账号联调。资源检查、相对链接、修改脚本及 `git diff --check` 随改动核对。遵循[编码规范](coding-standards.zh-CN.md)、[贡献流程](../CONTRIBUTING.zh-CN.md)与[组件开发文档](https://alonabot.github.io/)。
