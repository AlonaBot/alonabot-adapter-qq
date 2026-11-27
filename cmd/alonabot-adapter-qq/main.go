// alonabot-adapter-qq 命令运行 QQ 官方机器人适配器。
package main

import (
	"github.com/AlonaBot/AlonaBot/sdk"
	qq "github.com/AlonaBot/alonabot-adapter-qq"
	"log/slog"
	"os"
)

func main() {
	if err := sdk.RunAdapter(qq.New()); err != nil {
		slog.Error("QQ adapter stopped", "error", err)
		os.Exit(1)
	}
}
