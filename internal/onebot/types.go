// Package onebot holds OneBot 11 protocol value types and conversions.
package onebot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Segment is a single message segment, e.g. {"type":"text","data":{"text":"hi"}}.
type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func Text(s string) Segment {
	return Segment{Type: "text", Data: map[string]any{"text": s}}
}

func At(qq string) Segment {
	return Segment{Type: "at", Data: map[string]any{"qq": qq}}
}

func Face(id string) Segment {
	return Segment{Type: "face", Data: map[string]any{"id": id}}
}

func Image(url, file string) Segment {
	seg := Segment{Type: "image", Data: map[string]any{}}
	if url != "" {
		seg.Data["url"] = url
	}
	if file != "" {
		seg.Data["file"] = file
	}
	return seg
}

func Record(url, file string) Segment {
	seg := Segment{Type: "record", Data: map[string]any{}}
	if url != "" {
		seg.Data["url"] = url
	}
	if file != "" {
		seg.Data["file"] = file
	}
	return seg
}

func Video(url, file string) Segment {
	seg := Segment{Type: "video", Data: map[string]any{}}
	if url != "" {
		seg.Data["url"] = url
	}
	if file != "" {
		seg.Data["file"] = file
	}
	return seg
}

// ParseSegments accepts an OneBot 11 "message" value, in either array form or
// CQ-code string form, and normalises it to a segment slice.
func ParseSegments(raw any) ([]Segment, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return ParseCQ(v), nil
	case []Segment:
		return v, nil
	case []any:
		out := make([]Segment, 0, len(v))
		for _, item := range v {
			switch t := item.(type) {
			case Segment:
				out = append(out, t)
			case map[string]any:
				typ, _ := t["type"].(string)
				data, _ := t["data"].(map[string]any)
				if data == nil {
					data = map[string]any{}
				}
				out = append(out, Segment{Type: typ, Data: normalizeData(data)})
			case string:
				out = append(out, Text(t))
			default:
				return nil, fmt.Errorf("unsupported message element: %T", item)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported message type: %T", raw)
	}
}

func normalizeData(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case float64:
			// Integers arrive as float64 from encoding/json; keep them clean.
			if t == float64(int64(t)) {
				out[k] = int64(t)
				continue
			}
			out[k] = t
		default:
			out[k] = v
		}
	}
	return out
}

// ---- CQ code conversion ----

var cqEscaper = strings.NewReplacer(
	"&", "&#38;",
	"[", "&#91;",
	"]", "&#93;",
	",", "&#44;",
)

var cqUnescaper = strings.NewReplacer(
	"&#91;", "[",
	"&#93;", "]",
	"&#44;", ",",
	"&#38;", "&",
)

func escapeCQ(s string) string { return cqEscaper.Replace(s) }

func unescapeCQ(s string) string { return cqUnescaper.Replace(s) }

// ToCQ renders segments as an OneBot 11 CQ-code string.
func ToCQ(segs []Segment) string {
	var b strings.Builder
	for _, seg := range segs {
		if seg.Type == "text" {
			text, _ := seg.Data["text"].(string)
			b.WriteString(escapeCQ(text))
			continue
		}
		b.WriteString("[CQ:")
		b.WriteString(seg.Type)
		for _, k := range sortedKeys(seg.Data) {
			v := seg.Data[k]
			val := stringify(v)
			if val == "" {
				continue
			}
			b.WriteString(",")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(escapeCQ(val))
		}
		b.WriteString("]")
	}
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Keep common fields first for readable output.
	order := map[string]int{"url": -3, "file": -2, "qq": -1, "id": 0}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0; j-- {
			a, b := keys[j-1], keys[j]
			if order[a] > order[b] || (order[a] == order[b] && a > b) {
				keys[j-1], keys[j] = b, a
			}
		}
	}
	return keys
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// ParseCQ converts a CQ-code string into segments.
func ParseCQ(s string) []Segment {
	var out []Segment
	appendText := func(raw string) {
		if raw == "" {
			return
		}
		out = append(out, Text(unescapeCQ(raw)))
	}
	for len(s) > 0 {
		i := strings.Index(s, "[CQ:")
		if i < 0 {
			appendText(s)
			break
		}
		appendText(s[:i])
		rest := s[i+4:]
		end := strings.Index(rest, "]")
		if end < 0 {
			appendText(s[i:])
			break
		}
		body := rest[:end]
		parts := strings.Split(body, ",")
		seg := Segment{Type: strings.TrimSpace(parts[0]), Data: map[string]any{}}
		for _, kv := range parts[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			seg.Data[strings.TrimSpace(k)] = unescapeCQ(v)
		}
		out = append(out, seg)
		s = rest[end+1:]
	}
	return out
}

// PlainText extracts human-readable text from segments, optionally rendering
// non-text segments as readable placeholders.
func PlainText(segs []Segment) string {
	var b strings.Builder
	for _, seg := range segs {
		switch seg.Type {
		case "text":
			text, _ := seg.Data["text"].(string)
			b.WriteString(text)
		case "at":
			qq := stringify(seg.Data["qq"])
			if qq == "all" {
				b.WriteString("@全体成员")
			} else {
				b.WriteString("@" + qq)
				// Separate mentions so they cannot swallow following text.
				b.WriteString(" ")
			}
		case "face":
			b.WriteString("[表情]")
		case "image":
			b.WriteString("[图片]")
		case "record":
			b.WriteString("[语音]")
		case "video":
			b.WriteString("[视频]")
		case "file":
			b.WriteString("[文件]")
		case "reply":
			// Telegram-style reply markers would leak into QQ text.
		case "json", "xml":
			b.WriteString("[卡片消息]")
		default:
			b.WriteString("[" + seg.Type + "]")
		}
	}
	return strings.TrimSpace(b.String())
}

// ---- Event ----

// Event mirrors the OneBot 11 event envelope.
type Event struct {
	Time        int64  `json:"time"`
	SelfID      int64  `json:"self_id"`
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type,omitempty"`
	SubType     string `json:"sub_type,omitempty"`
	MessageID   int64  `json:"message_id,omitempty"`
	GroupID     int64  `json:"group_id,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	TargetID    int64  `json:"target_id,omitempty"`
	Message     any    `json:"message,omitempty"`
	RawMessage  string `json:"raw_message,omitempty"`
	MessageSeq  int64  `json:"message_seq,omitempty"`
	Font        int    `json:"font,omitempty"`
	Sender      Sender `json:"sender,omitempty"`
	NoticeType  string `json:"notice_type,omitempty"`
	RequestType string `json:"request_type,omitempty"`
	OperatorID  int64  `json:"operator_id,omitempty"`
}

type Sender struct {
	UserID   int64  `json:"user_id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Card     string `json:"card,omitempty"`
	Role     string `json:"role,omitempty"`
	Title    string `json:"title,omitempty"`
}

// Action is an incoming OneBot API call.
type Action struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
	Echo   any            `json:"echo,omitempty"`
}

// APIResponse is the standard OneBot API reply.
type APIResponse struct {
	Status  string `json:"status"`
	RetCode int    `json:"retcode"`
	Data    any    `json:"data"`
	Msg     string `json:"msg,omitempty"`
	Wording string `json:"wording,omitempty"`
	Echo    any    `json:"echo,omitempty"`
}

func OK(echo any, data any) APIResponse {
	return APIResponse{Status: "ok", RetCode: 0, Data: data, Echo: echo}
}

func Failed(echo any, retCode int, msg string) APIResponse {
	return APIResponse{Status: "failed", RetCode: retCode, Data: nil, Msg: msg, Wording: msg, Echo: echo}
}

func MakeFailed(echo any, msg string) APIResponse { return Failed(echo, 1400, msg) }

// ErrActionNotFound marks an API the implementation does not provide. The
// transport maps it to the spec's "API does not exist" responses: HTTP 404,
// and retcode 1404 over WebSocket.
type ErrActionNotFound struct{ Action string }

func (e *ErrActionNotFound) Error() string { return "不支持的接口: " + e.Action }

// ErrUnsupported marks a known API that this platform cannot provide. It is a
// business failure (HTTP 200 with a non-zero retcode), not a missing API.
type ErrUnsupported struct {
	Action string
	Reason string
}

func (e *ErrUnsupported) Error() string {
	if e.Reason == "" {
		return "接口不受平台支持: " + e.Action
	}
	return e.Reason
}

// Param helpers tolerate the loose typing OneBot clients send.

func StrParam(p map[string]any, key string) string {
	v, ok := p[key]
	if !ok {
		return ""
	}
	return stringify(v)
}

func IntParam(p map[string]any, key string) int64 {
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0
		}
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	return 0
}

func BoolParam(p map[string]any, key string) bool {
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case float64:
		return v != 0
	}
	return false
}
