# Develop the QQ adapter

[简体中文](development.zh-CN.md)

Run from the repository root using the Go version in `go.mod`, managed locally with asdf. Install a CLI matching the selected SDK before testing:

```fish
go mod download
env GOTOOLCHAIN=(go env GOVERSION) go -C (go list -m -f '{{.Dir}}' github.com/AlonaBot/AlonaBot) install ./cmd/alonabot
go build ./...
go vet ./...
go test -race -count=1 ./...
alonabot check .
alonabot test .
```

Add the Go bin directory to PATH. CI/releases retain the template's `go get github.com/AlonaBot/AlonaBot@latest` policy, followed by building the matching CLI. Review module changes after local SDK upgrades.

## Source entry points

- `qq.go`: public `New() adapter.Adapter`, preserving the component resource identity.
- `internal/qq/`: account lifecycle, HTTP client, Webhook/WebSocket, events, media and send planning.
- `cmd/alonabot-adapter-qq/`: calls `sdk.RunAdapter`.
- `adapter_config_schema.json`, `plugin.yaml`: account schema and identity, embedded by SDK build overlays.
- `adapter_test.go`: generates an isolated library-mode app and probe plugin at runtime, exercising real Core/SDK, the HTTP proxy and a local QQ simulator for both transports, four scenes and callback routing.

The integration test requires the matching `alonabot` CLI, uses temporary data directories and never contacts real QQ. Ignored `*_local_test.go` files hold temporary unit/failure checks. Commit only cross-component integration tests; report committed test lines and local results in the PR.

## Live testing and cross-compilation

Configure a test account using [usage](usage.md), then run:

```fish
alonabot run . --data-dir ./data
```

This command opens real platform connections using your local credentials. Ordinary automated tests and `alonabot test .` remain offline.

```fish
for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64
    set -l parts (string split / $target)
    env GOOS=$parts[1] GOARCH=$parts[2] CGO_ENABLED=0 go build ./...
end
```

Cross-compilation proves builds only; live QQ behavior requires later testing. Inspect resources, relative links, changed scripts and `git diff --check` as applicable. Follow the [coding standards](coding-standards.md), [contribution workflow](../CONTRIBUTING.md) and [component developer documentation](https://alonabot.github.io/).
