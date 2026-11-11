package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"qqbotonebot/internal/onebot"
)

// traceLimit caps how much of a payload goes into the console log. Long
// messages would otherwise flood the log view and make it unreadable.
const traceLimit = 600

// traceEnabled reports whether this listener should emit send/receive traces.
func (rt *listenerRuntime) traceEnabled() bool { return rt.cfg.DebugLog }

// traceReceive logs an inbound OneBot API call with its parameters.
func (rt *listenerRuntime) traceReceive(transport, action string, act onebot.Action, echo any) {
	if !rt.traceEnabled() {
		return
	}
	params := compactJSON(act.Params)
	rt.app.Log("debug", fmt.Sprintf(
		"[%s/#%d %s] ← 收到动作 %s 参数=%s echo=%s",
		rt.cfg.Name, rt.cfg.ID, transport, action, params, ellipsis(anyToString(echo)),
	))
}

// traceReply logs the response sent back to the OneBot client.
func (rt *listenerRuntime) traceReply(transport, action string, resp onebot.APIResponse) {
	if !rt.traceEnabled() {
		return
	}
	status := "ok"
	if resp.Status != "ok" {
		status = fmt.Sprintf("failed(%d) %s", resp.RetCode, resp.Msg)
	}
	var data string
	if resp.Data != nil {
		data = " data=" + compactJSON(resp.Data)
	}
	rt.app.Log("debug", fmt.Sprintf(
		"[%s/#%d %s] → 返回 %s%s echo=%s",
		rt.cfg.Name, rt.cfg.ID, transport, status, data, ellipsis(anyToString(resp.Echo)),
	))
}

// traceEvent logs an outbound event, summarising the message segments.
func (rt *listenerRuntime) traceEvent(transport string, ev onebot.Event) {
	if !rt.traceEnabled() {
		return
	}
	var b strings.Builder
	switch ev.PostType {
	case "message":
		segs, _ := onebot.ParseSegments(ev.Message)
		types := make([]string, 0, len(segs))
		for _, s := range segs {
			types = append(types, s.Type)
			// Images/records carry URLs worth seeing while debugging media.
			if s.Type == "image" || s.Type == "record" || s.Type == "video" || s.Type == "file" {
				if u := onebot.StrParam(s.Data, "url"); u != "" {
					types[len(types)-1] += "(" + ellipsis(u) + ")"
				}
			}
		}
		b.WriteString(fmt.Sprintf("type=%s msg_type=%s group=%d user=%d msg_id=%d segs=[%s] raw=%q",
			ev.PostType, ev.MessageType, ev.GroupID, ev.UserID, ev.MessageID,
			strings.Join(types, " "), ellipsis(ev.RawMessage)))
	default:
		b.WriteString(fmt.Sprintf("type=%s notice=%s request=%s group=%d user=%d",
			ev.PostType, ev.NoticeType, ev.RequestType, ev.GroupID, ev.UserID))
	}
	rt.app.Log("debug", fmt.Sprintf("[%s/#%d %s] → 推送事件 %s",
		rt.cfg.Name, rt.cfg.ID, transport, b.String()))
}

// traceRaw logs connection-level payloads (WS frames, HTTP posts) when the
// caller wants the exact bytes.
func (rt *listenerRuntime) traceRaw(transport, dir, payload string) {
	if !rt.traceEnabled() {
		return
	}
	rt.app.Log("debug", fmt.Sprintf("[%s/#%d %s] %s %s",
		rt.cfg.Name, rt.cfg.ID, transport, dir, ellipsis(payload)))
}

// compactJSON renders a value as single-line JSON for logging.
func compactJSON(v any) string {
	if v == nil {
		return "{}"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSpace(buf.String())
}

// anyToString renders primitives without the JSON quoting noise.
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case json.Number:
		return t.String()
	default:
		return compactJSON(v)
	}
}

// ellipsis truncates long values and marks the cut.
func ellipsis(s string) string {
	r := []rune(s)
	if len(r) <= traceLimit {
		return s
	}
	return string(r[:traceLimit]) + fmt.Sprintf("…(%d 字)", len(r))
}

// redactToken hides a token in traces while keeping it identifiable.
func redactToken(tok string) string {
	if tok == "" {
		return "(无)"
	}
	r := []rune(tok)
	if len(r) <= 6 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + "…" + string(r[len(r)-2:])
}
