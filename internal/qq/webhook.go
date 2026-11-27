package qq

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (a *Adapter) webhook(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	i := a.instances[strings.TrimPrefix(r.URL.Path, "/webhook/")]
	a.mu.RUnlock()
	if i == nil || i.config.Mode != "webhook" {
		http.Error(w, "QQ webhook instance is unavailable", http.StatusNotFound)
		return
	}
	ctx, finish, err := i.operation(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer finish()
	interrupt := context.AfterFunc(ctx, func() {
		// Server 的 Body.Read 由套接字读取驱动，取消 context 还需打断实际读取。
		if err := http.NewResponseController(w).SetReadDeadline(time.Now()); err != nil && !errors.Is(err, http.ErrNotSupported) {
			slog.Warn("QQ webhook read interruption failed", "error", err)
		}
		if err := r.Body.Close(); err != nil {
			slog.Warn("QQ webhook body close failed", "error", err)
		}
	})
	defer interrupt()
	// 1. 验证原始请求体，解析前确认请求由本账号的 QQ 平台签发。
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read QQ webhook body failed", http.StatusBadRequest)
		return
	}
	signature, err := hex.DecodeString(r.Header.Get("X-Signature-Ed25519"))
	timestamp := r.Header.Get("X-Signature-Timestamp")
	if err != nil || timestamp == "" || !ed25519.Verify(i.key.Public().(ed25519.PublicKey), append([]byte(timestamp), body...), signature) {
		http.Error(w, "invalid QQ webhook signature", http.StatusUnauthorized)
		return
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid QQ webhook payload", http.StatusBadRequest)
		return
	}
	// 2. 地址验证签名使用 event_ts 与 plain_token；普通事件在 Core 接纳后确认。
	switch p.Op {
	case opValidation:
		var challenge struct {
			PlainToken string `json:"plain_token"`
			EventTS    string `json:"event_ts"`
		}
		if err := json.Unmarshal(p.Body, &challenge); err != nil || challenge.PlainToken == "" || challenge.EventTS == "" {
			http.Error(w, "invalid QQ webhook challenge", http.StatusBadRequest)
			return
		}
		sig := ed25519.Sign(i.key, []byte(challenge.EventTS+challenge.PlainToken))
		writeJSON(w, map[string]string{"plain_token": challenge.PlainToken, "signature": hex.EncodeToString(sig)})
	case opDispatch:
		if err := i.dispatch(ctx, p, body); err != nil {
			slog.Error("QQ webhook event delivery failed", "instance_id", i.id, "error", i.client.safe(err))
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]int{"op": opCallbackACK, "d": 1})
			return
		}
		writeJSON(w, map[string]int{"op": opCallbackACK, "d": 0})
	case opHeartbeat:
		writeJSON(w, map[string]any{"op": opHeartbeatACK, "d": p.Body})
	default:
		http.Error(w, "unsupported QQ webhook opcode", http.StatusBadRequest)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("QQ webhook response write failed", "error", err)
	}
}
