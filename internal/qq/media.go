package qq

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AlonaBot/AlonaBot/sdk/message"
)

// SDK 的媒体读取协议单次最多 1 MiB。
const mediaChunkBytes = 1 << 20

func (i *instance) incomingSource(file attachment) (message.MediaSource, error) {
	address, err := url.Parse(file.URL)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil {
		return message.MediaSource{}, errors.New("QQ attachment URL must be HTTPS without user credentials")
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return message.MediaSource{}, err
	}
	// fileID 自带签名，Core 再交回时可验证来源且无需常驻 URL 缓存。
	signature := ed25519.Sign(i.key, encoded)
	fileID := base64.RawURLEncoding.EncodeToString(append(signature, encoded...))
	mime := file.MIME
	if mime == "voice" {
		mime = "audio/silk"
	}
	return message.MediaSource{Incoming: &message.IncomingMedia{Token: fileID, Size: file.Size, MIME: mime, Readability: message.Readable}}, nil
}

// ReadPlatformFile 验证平台文件句柄，按 SDK 请求的范围读取 QQ 附件。
func (i *instance) ReadPlatformFile(ctx context.Context, fileID string, offset uint64, length uint32) ([]byte, uint64, bool, error) {
	ctx, finish, err := i.operation(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	defer finish()
	if length == 0 || length > mediaChunkBytes {
		return nil, 0, false, errors.New("media length must be between 1 and 1048576 bytes")
	}
	file, err := i.platformAttachment(fileID)
	if err != nil {
		return nil, 0, false, err
	}
	response, err := i.fetch(ctx, file.URL, fmt.Sprintf("bytes=%d-%d", offset, offset+uint64(length)-1))
	if err != nil {
		return nil, 0, false, err
	}
	defer response.Body.Close()
	total := file.Size
	if response.StatusCode == http.StatusPartialContent {
		_, tail, ok := strings.Cut(response.Header.Get("Content-Range"), "/")
		if !ok {
			return nil, 0, false, errors.New("QQ attachment response has no Content-Range total")
		}
		total, err = strconv.ParseUint(tail, 10, 64)
		if err != nil {
			return nil, 0, false, fmt.Errorf("parse QQ attachment length: %w", err)
		}
	} else if response.StatusCode == http.StatusOK {
		if response.ContentLength >= 0 {
			total = uint64(response.ContentLength)
		}
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, response.Body, int64(offset)); err != nil {
				if errors.Is(err, io.EOF) {
					return nil, total, true, nil
				}
				return nil, 0, false, err
			}
		}
	} else {
		return nil, 0, false, fmt.Errorf("QQ attachment HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(length)))
	if err != nil {
		return nil, 0, false, err
	}
	eof := len(data) < int(length) || (total > 0 && offset+uint64(len(data)) >= total)
	return data, total, eof, nil
}

func (i *instance) fetch(ctx context.Context, address, byteRange string) (*http.Response, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("media URL must be HTTPS without user credentials")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	client := *i.client.http
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("media redirect must use HTTPS without user credentials")
		}
		// 保留标准 HTTP 客户端的重定向环限制。
		if len(via) >= 10 {
			return errors.New("too many media redirects")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		var addressError *url.Error
		if errors.As(err, &addressError) {
			return nil, fmt.Errorf("fetch QQ media: %w", addressError.Err)
		}
		return nil, err
	}
	return response, nil
}

// coreReader 逐块消费本次发送的只读令牌，避免把文件整体装入内存。
type coreReader struct {
	instance *instance
	ctx      context.Context
	token    string
	offset   uint64
	pending  []byte
	eof      bool
}

func (r *coreReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		data, _, eof, err := r.instance.core.ReadMedia(r.ctx, r.token, r.offset, mediaChunkBytes)
		if err != nil {
			return 0, err
		}
		if len(data) == 0 && !eof {
			return 0, io.ErrNoProgress
		}
		r.pending, r.eof = data, eof
		r.offset += uint64(len(data))
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if n == 0 && r.eof {
		return 0, io.EOF
	}
	return n, nil
}

func (i *instance) mediaReader(ctx context.Context, source message.MediaSource) (io.ReadCloser, error) {
	switch {
	case source.PlatformFile != "":
		file, err := i.platformAttachment(source.PlatformFile)
		if err != nil {
			return nil, err
		}
		response, err := i.fetch(ctx, file.URL, "")
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return nil, fmt.Errorf("QQ platform media HTTP %d", response.StatusCode)
		}
		return response.Body, nil
	case source.MediaRef != "":
		return io.NopCloser(&coreReader{instance: i, ctx: ctx, token: source.MediaRef}), nil
	case source.InlineBase64 != "":
		return io.NopCloser(base64.NewDecoder(base64.StdEncoding, strings.NewReader(source.InlineBase64))), nil
	case source.HTTPSURL != "":
		response, err := i.fetch(ctx, source.HTTPSURL, "")
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return nil, fmt.Errorf("media download HTTP %d", response.StatusCode)
		}
		return response.Body, nil
	default:
		return nil, errors.New("media source has no Core token, HTTPS URL or base64 content")
	}
}

func (i *instance) platformAttachment(fileID string) (attachment, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(fileID)
	if err != nil || len(encoded) <= ed25519.SignatureSize {
		return attachment{}, errors.New("invalid QQ platform file handle")
	}
	signature, body := encoded[:ed25519.SignatureSize], encoded[ed25519.SignatureSize:]
	if !ed25519.Verify(i.key.Public().(ed25519.PublicKey), body, signature) {
		return attachment{}, errors.New("invalid QQ platform file signature")
	}
	var file attachment
	if err := json.Unmarshal(body, &file); err != nil {
		return attachment{}, err
	}
	return file, nil
}
