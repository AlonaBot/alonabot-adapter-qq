package qq

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/AlonaBot/AlonaBot/sdk/message"
)

// renderText 将 Core 的通用 UTF-16 实体还原成 QQ Markdown，保留嵌套与 Unicode 边界。
func renderText(segment message.TextSegment) (string, error) {
	boundaries := map[uint32]int{0: 0}
	var units uint32
	for index, r := range segment.Text {
		boundaries[units] = index
		units += uint32(utf16.RuneLen(r))
	}
	boundaries[units] = len(segment.Text)
	entities := append([]message.TextEntity(nil), segment.Entities...)
	sort.SliceStable(entities, func(a, b int) bool {
		if entities[a].OffsetUTF16 != entities[b].OffsetUTF16 {
			return entities[a].OffsetUTF16 < entities[b].OffsetUTF16
		}
		return entities[a].LengthUTF16 > entities[b].LengthUTF16
	})
	for _, entity := range entities {
		end := uint64(entity.OffsetUTF16) + uint64(entity.LengthUTF16)
		_, startOK := boundaries[entity.OffsetUTF16]
		_, endOK := boundaries[uint32(end)]
		if !startOK || !endOK || end > uint64(units) || entity.LengthUTF16 == 0 {
			return "", errors.New("QQ text entity has invalid UTF-16 boundaries")
		}
	}
	var render func(uint32, uint32, []message.TextEntity) (string, error)
	render = func(start, end uint32, nested []message.TextEntity) (string, error) {
		var out strings.Builder
		position := start
		for index := 0; index < len(nested); {
			entity := nested[index]
			finish := entity.OffsetUTF16 + entity.LengthUTF16
			if entity.OffsetUTF16 < position || finish > end {
				return "", errors.New("QQ text entities overlap without nesting")
			}
			out.WriteString(escapeMarkdown(segment.Text[boundaries[position]:boundaries[entity.OffsetUTF16]]))
			next := index + 1
			for next < len(nested) && nested[next].OffsetUTF16 < finish {
				next++
			}
			content, err := render(entity.OffsetUTF16, finish, nested[index+1:next])
			if err != nil {
				return "", err
			}
			raw := segment.Text[boundaries[entity.OffsetUTF16]:boundaries[finish]]
			switch entity.Type {
			case message.EntityBold:
				content = "**" + content + "**"
			case message.EntityItalic:
				content = "*" + content + "*"
			case message.EntityStrikethrough:
				content = "~~" + content + "~~"
			case message.EntityCode:
				fence := backtickFence(raw, 1)
				content = fence + " " + raw + " " + fence
			case message.EntityPre:
				if strings.ContainsAny(entity.Language, "\r\n`") {
					return "", errors.New("invalid QQ code block language")
				}
				fence := backtickFence(raw, 3)
				content = "\n" + fence + entity.Language + "\n" + raw + "\n" + fence + "\n"
			case message.EntityTextLink:
				content = "[" + content + "](" + strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(entity.URL) + ")"
			case message.EntityTextMention:
				content = "<@" + entity.ExternalUserID + ">"
			default:
				return "", fmt.Errorf("QQ does not support text entity %d", entity.Type)
			}
			out.WriteString(content)
			position = finish
			index = next
		}
		out.WriteString(escapeMarkdown(segment.Text[boundaries[position]:boundaries[end]]))
		return out.String(), nil
	}
	return render(0, units, entities)
}

func escapeMarkdown(text string) string {
	var out strings.Builder
	for _, r := range text {
		if strings.ContainsRune("\\`*_{}[]<>()#+-.!|~", r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func backtickFence(text string, minimum int) string {
	longest, current := 0, 0
	for _, r := range text {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	if longest >= minimum {
		minimum = longest + 1
	}
	return strings.Repeat("`", minimum)
}
