package qq

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	"github.com/AlonaBot/AlonaBot/sdk/message"
)

const (
	// QQ 被动回复有效期为 5 分钟；超期上下文不继续用于被动发送。
	replyWindow = 5 * time.Minute
	// QQ file_data 直接上传上限为 5 MiB，较大媒体使用 Core 临时 URL。
	directMediaMaxBytes        = 5 << 20
	keyboardMaxRows            = 5
	keyboardMaxColumns         = 5
	keyboardMaxLabelCharacters = 10
)

type sendPart struct {
	indexes   []uint32
	text      string
	markdown  bool
	media     *message.MediaSource
	filename  string
	fileType  int
	keyboard  map[string]any
	reference string
}

func pathID(id string) string { return url.PathEscape(id) }

func messagePath(scene, id string) string {
	switch scene {
	case "group":
		return "/v2/groups/" + pathID(id) + "/messages"
	case "c2c":
		return "/v2/users/" + pathID(id) + "/messages"
	case "channel":
		return "/channels/" + pathID(id) + "/messages"
	default:
		return "/dms/" + pathID(id) + "/messages"
	}
}

// Send 在首个平台请求前生成有序发送计划，逐项保留成功、拒绝或结果未知。
func (i *instance) Send(ctx context.Context, to string, chain message.Chain) (*message.SendResult, *errcode.Error) {
	result := &message.SendResult{Session: to, AdapterInstanceID: i.id, Status: message.SendRejected}
	ctx, finish, err := i.operation(ctx)
	if err != nil {
		return result, errcode.FailedPrecondition.New(err.Error())
	}
	defer finish()
	scene, id, err := parseTarget(to)
	if err != nil {
		return result, errcode.InvalidArgument.New(err.Error())
	}
	parts, safe := plan(chain, to, i.id, scene)
	if safe != nil {
		return result, safe
	}
	// 1. 串行分配消息序号，发送未知也占用该序号，避免后续操作复用。
	i.sendMu.Lock()
	defer i.sendMu.Unlock()
	var latest replyContext
	if err := i.readState("reply", to, &latest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, errcode.Internal.New(err.Error())
	}
	reply := latest
	if parts[0].reference != "" {
		if err := i.readState("message", parts[0].reference, &reply); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return result, errcode.Internal.New(err.Error())
			}
			if scene == "group" || scene == "c2c" {
				return result, errcode.FailedPrecondition.New("QQ reply requires a known incoming message")
			}
			reply = replyContext{}
		}
	}
	if reply.Session != "" && reply.Session != to {
		return result, errcode.InvalidArgument.New("QQ reply context belongs to another session")
	}
	updateLatest := reply.MessageID == latest.MessageID && reply.EventID == latest.EventID
	if time.Since(reply.At) > replyWindow {
		reply = replyContext{}
	}
	// 2. 预取全部媒体来源；来源错误在产生 QQ 发送副作用前返回。
	urls := make([]string, len(parts))
	for index, part := range parts {
		if part.media == nil {
			continue
		}
		if (scene == "channel" || scene == "dm") && part.media.HTTPSURL == "" {
			continue
		}
		if part.media.InlineBase64 != "" {
			if base64.StdEncoding.DecodedLen(len(part.media.InlineBase64)) > directMediaMaxBytes+2 {
				return result, errcode.Unsupported.New("QQ inline media exceeds 5 MiB; upload to Core media first")
			}
			decoded, err := base64.StdEncoding.DecodeString(part.media.InlineBase64)
			if err != nil {
				return result, errcode.InvalidArgument.New("invalid inline media base64")
			}
			if len(decoded) > directMediaMaxBytes {
				return result, errcode.Unsupported.New("QQ inline media exceeds 5 MiB; upload to Core media first")
			}
			continue
		}
		address, err := i.mediaURL(ctx, *part.media)
		if err != nil {
			return result, errcode.FailedPrecondition.New(i.client.safe(err).Error())
		}
		urls[index] = address
	}
	// 3. 每个物理操作保存状态，首次失败后其余操作保留为未尝试。
	result.Status = message.SendAccepted
	var firstError *errcode.Error
	for index, part := range parts {
		receipt := message.PartResult{PartIndex: uint32(index), SegmentIndexes: part.indexes, Status: message.PartNotAttempted}
		if firstError != nil {
			result.PartResults = append(result.PartResults, receipt)
			continue
		}
		body := map[string]any{}
		if scene == "group" || scene == "c2c" {
			body["msg_type"] = 0
		}
		if part.markdown {
			body["markdown"] = map[string]string{"content": part.text}
			body["msg_type"] = 2
		} else if part.text != "" {
			body["content"] = part.text
		}
		if part.keyboard != nil {
			body["keyboard"] = part.keyboard
		}
		if index == 0 && part.reference != "" && (scene == "channel" || scene == "dm") {
			body["message_reference"] = map[string]any{"message_id": part.reference, "ignore_get_message_error": false}
		}
		if reply.MessageID != "" {
			body["msg_id"] = reply.MessageID
		}
		if reply.EventID != "" {
			body["event_id"] = reply.EventID
		}
		if reply.MessageID != "" || reply.EventID != "" {
			reply.Sequence++
			body["msg_seq"] = reply.Sequence
			err = nil
			if updateLatest {
				err = i.writeState("reply", to, reply)
			}
			if err == nil && reply.MessageID != "" {
				err = i.writeState("message", reply.MessageID, reply)
			}
			if err != nil {
				firstError = errcode.Internal.New(err.Error())
				receipt.Status = message.PartNotAttempted
				receipt.Error = firstError
				result.PartResults = append(result.PartResults, receipt)
				continue
			}
		}
		var sent struct {
			ID string `json:"id"`
		}
		var sendErr error
		attempted := false
		if part.media != nil {
			if scene == "group" || scene == "c2c" {
				var uploaded struct {
					FileInfo string `json:"file_info"`
				}
				uploadPath := strings.TrimSuffix(messagePath(scene, id), "/messages") + "/files"
				upload := map[string]any{"file_type": part.fileType, "srv_send_msg": false}
				if part.media.InlineBase64 != "" {
					upload["file_data"] = part.media.InlineBase64
					name := part.filename
					if name == "" {
						name = "media"
					}
					upload["file_name"] = name
				} else {
					upload["url"] = urls[index]
					if part.filename != "" {
						upload["file_name"] = part.filename
					}
				}
				sendErr = i.client.call(ctx, http.MethodPost, uploadPath, upload, &uploaded)
				if sendErr == nil && uploaded.FileInfo == "" {
					sendErr = errors.New("QQ media upload response has no file_info")
				}
				if sendErr == nil {
					body["msg_type"] = 7
					body["media"] = map[string]string{"file_info": uploaded.FileInfo}
				}
			} else if urls[index] != "" {
				body["image"] = urls[index]
			}
		}
		if sendErr == nil {
			attempted = true
			if part.media != nil && (scene == "channel" || scene == "dm") && urls[index] == "" {
				sendErr = i.sendImage(ctx, messagePath(scene, id), body, *part.media, &sent)
			} else {
				sendErr = i.client.call(ctx, http.MethodPost, messagePath(scene, id), body, &sent)
			}
		}
		if sendErr != nil {
			firstError, receipt.Status = sendError(i.client, sendErr, attempted)
			receipt.Error = firstError
			if hasAccepted(result) {
				result.Status = message.SendPartial
			} else if receipt.Status == message.PartUnknown {
				result.Status = message.SendUnknown
			} else {
				result.Status = message.SendRejected
			}
		} else {
			receipt.Status = message.PartAccepted
			receipt.MessageIDsUnavailable = sent.ID == ""
			if sent.ID != "" {
				receipt.PlatformMessageIDs = []string{sent.ID}
				result.PlatformMessageIDs = append(result.PlatformMessageIDs, sent.ID)
				if result.PrimaryPlatformMessageID == "" {
					result.PrimaryPlatformMessageID = sent.ID
				}
			}
			result.ProviderAcceptedAt = time.Now()
			if part.keyboard != nil && sent.ID != "" {
				if err := i.writeState("keyboard", sent.ID, to); err != nil {
					firstError = errcode.Internal.New(err.Error())
					receipt.Error = firstError
					result.Status = message.SendPartial
				}
			}
		}
		result.PartResults = append(result.PartResults, receipt)
	}
	if firstError != nil && result.Status == message.SendAccepted {
		if hasAccepted(result) {
			result.Status = message.SendPartial
		} else {
			result.Status = message.SendRejected
		}
	}
	return result, firstError
}

func sendError(c *client, err error, attempted bool) (*errcode.Error, message.PartStatus) {
	var rejected *apiError
	code, status := errcode.Unavailable, message.PartUnknown
	if !attempted {
		status = message.PartRejected
	}
	if errors.As(err, &rejected) {
		// QQ 5xx 无法证明业务未执行，保留 UNKNOWN，4xx 或明确业务拒绝记 REJECTED。
		if rejected.status < 500 {
			status = message.PartRejected
			code = errcode.FailedPrecondition
		}
		switch rejected.status {
		case 401:
			code = errcode.Unauthenticated
		case 403:
			code = errcode.PermissionDenied
		case 429:
			code = errcode.ResourceExhausted
		}
	}
	if errors.Is(err, context.Canceled) {
		code = errcode.Cancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = errcode.DeadlineExceeded
	}
	return code.New(c.safe(err).Error()), status
}

func (i *instance) mediaURL(ctx context.Context, source message.MediaSource) (string, error) {
	if source.PlatformFile != "" {
		file, err := i.platformAttachment(source.PlatformFile)
		if err != nil {
			return "", err
		}
		return file.URL, nil
	}
	if source.HTTPSURL != "" {
		parsed, err := url.Parse(source.HTTPSURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return "", errors.New("outgoing media URL must be HTTPS without user credentials")
		}
		return source.HTTPSURL, nil
	}
	if source.MediaRef == "" {
		return "", errors.New("outgoing media requires an HTTPS URL or Core media token")
	}
	address, _, err := i.core.MediaURL(ctx, source.MediaRef, "")
	return address, err
}

func (i *instance) sendImage(ctx context.Context, path string, fields map[string]any, source message.MediaSource, out any) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, err := i.mediaReader(ctx, source)
	if err != nil {
		return err
	}
	input, output := io.Pipe()
	form := multipart.NewWriter(output)
	done := make(chan error, 1)
	go func() {
		var writeErr error
		for key, value := range fields {
			var text string
			if s, ok := value.(string); ok {
				text = s
			} else {
				encoded, err := json.Marshal(value)
				if err != nil {
					writeErr = err
					break
				}
				text = string(encoded)
			}
			if err := form.WriteField(key, text); err != nil {
				writeErr = err
				break
			}
		}
		if writeErr == nil {
			var part io.Writer
			part, writeErr = form.CreateFormFile("file_image", "image")
			if writeErr == nil {
				_, writeErr = io.Copy(part, reader)
			}
		}
		if writeErr == nil {
			writeErr = form.Close()
		}
		_ = output.CloseWithError(writeErr)
		done <- writeErr
	}()
	requestErr := i.client.upload(ctx, http.MethodPost, path, form.FormDataContentType(), input, out)
	cancel() // 平台提前回应时打断源读取，再等待生产协程结束。
	closeErr := reader.Close()
	_ = input.CloseWithError(requestErr) // 平台提前拒绝时解除写协程的阻塞。
	writeErr := <-done
	return errors.Join(requestErr, writeErr, closeErr)
}

func plan(chain message.Chain, to, instanceID, scene string) ([]sendPart, *errcode.Error) {
	var parts []sendPart
	var reference string
	var referenceIndex uint32
	var keyboard map[string]any
	var keyboardIndex uint32
	for index, segment := range chain {
		part := sendPart{indexes: []uint32{uint32(index)}}
		switch s := segment.(type) {
		case message.TextSegment:
			part.text, part.markdown = s.Text, s.Format == message.FormatMarkdown
			if len(s.Entities) > 0 {
				var err error
				part.text, err = renderText(s)
				if err != nil {
					return nil, errcode.Unsupported.New(err.Error())
				}
				part.markdown = true
			}
		case message.MarkdownSegment:
			part.text, part.markdown = s.Content, true
		case message.MentionSegment:
			part.text = "<@" + s.ExternalUserID + ">"
		case message.ImageSegment:
			source := s.Source
			part.media = &source
			part.fileType = 1
			part.text = s.Caption
		case message.VideoSegment:
			source := s.Source
			part.media = &source
			part.fileType = 2
			part.text = s.Caption
		case message.VoiceSegment:
			source := s.Source
			part.media = &source
			part.fileType = 3
			part.text = s.Caption
		case message.FileSegment:
			source := s.Source
			part.media = &source
			part.fileType = 4
			part.text = s.Caption
			part.filename = s.Filename
		case message.ReferenceSegment:
			if reference != "" {
				return nil, errcode.InvalidArgument.New("QQ supports one reference per message")
			}
			session, id, instance, err := message.ParseRef(s.Ref)
			if err != nil || session != to || (instance != "" && instance != instanceID) {
				return nil, errcode.InvalidArgument.New("QQ reference must belong to the same session and instance")
			}
			reference, referenceIndex = id, uint32(index)
			continue
		case message.ButtonsSegment:
			if keyboard != nil {
				return nil, errcode.InvalidArgument.New("QQ supports one keyboard per message")
			}
			var safe *errcode.Error
			keyboard, safe = renderKeyboard(s)
			if safe != nil {
				return nil, safe
			}
			keyboardIndex = uint32(index)
			continue
		default:
			return nil, errcode.Unsupported.Errorf("QQ segment %T is not supported", segment)
		}
		if part.media != nil && (scene == "channel" || scene == "dm") && part.fileType != 1 {
			return nil, errcode.Unsupported.New("QQ channel and channel DM endpoints support image attachments only")
		}
		if part.media == nil && part.text == "" {
			continue
		}
		// 相邻同格式文本合并，保留提及与正文顺序，媒体保持独立物理操作。
		if part.media == nil && len(parts) > 0 && parts[len(parts)-1].media == nil && parts[len(parts)-1].markdown == part.markdown {
			previous := &parts[len(parts)-1]
			previous.text += part.text
			previous.indexes = append(previous.indexes, part.indexes...)
		} else {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil, errcode.InvalidArgument.New("QQ message has no sendable content")
	}
	if reference != "" {
		parts[0].reference = reference
		parts[0].indexes = append(parts[0].indexes, referenceIndex)
	}
	if keyboard != nil {
		last := &parts[len(parts)-1]
		if last.media != nil {
			return nil, errcode.Unsupported.New("QQ keyboard requires final text or Markdown content")
		}
		if !last.markdown {
			last.text = escapeMarkdown(last.text)
			last.markdown = true
		}
		last.keyboard = keyboard
		last.indexes = append(last.indexes, keyboardIndex)
	}
	return parts, nil
}

func renderKeyboard(segment message.ButtonsSegment) (map[string]any, *errcode.Error) {
	if len(segment.Rows) == 0 || len(segment.Rows) > keyboardMaxRows {
		return nil, errcode.InvalidArgument.New("QQ keyboard requires 1 to 5 rows")
	}
	rows := make([]any, 0, len(segment.Rows))
	for rowIndex, row := range segment.Rows {
		if len(row) == 0 || len(row) > keyboardMaxColumns {
			return nil, errcode.InvalidArgument.New("QQ keyboard row requires 1 to 5 buttons")
		}
		buttons := make([]any, 0, len(row))
		for column, button := range row {
			if button.Label == "" || utf8.RuneCountInString(button.Label) > keyboardMaxLabelCharacters {
				return nil, errcode.InvalidArgument.New("QQ button label requires 1 to 10 characters")
			}
			actionType, data := 1, button.CoreToken
			if button.URL != "" {
				parsed, err := url.Parse(button.URL)
				if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
					return nil, errcode.InvalidArgument.New("QQ URL button requires an HTTP or HTTPS URL without user credentials")
				}
				actionType, data = 0, button.URL
			} else if data == "" {
				return nil, errcode.InvalidArgument.New("QQ callback button requires a Core token")
			}
			buttons = append(buttons, map[string]any{"id": strconv.Itoa(rowIndex) + "_" + strconv.Itoa(column), "render_data": map[string]any{"label": button.Label, "visited_label": button.Label, "style": 1}, "action": map[string]any{"type": actionType, "data": data, "permission": map[string]int{"type": 2}}})
		}
		rows = append(rows, map[string]any{"buttons": buttons})
	}
	return map[string]any{"content": map[string]any{"rows": rows}}, nil
}

func hasAccepted(result *message.SendResult) bool {
	for _, part := range result.PartResults {
		if part.Status == message.PartAccepted {
			return true
		}
	}
	return false
}
