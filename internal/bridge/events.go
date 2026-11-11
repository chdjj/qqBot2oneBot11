package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

// contentToSegments turns QQ's plain "content" string into OneBot segments,
// recognising the inline <qqbot-at-...> / <emoji> markers the platform may emit.
func contentToSegments(content string) []onebot.Segment {
	if content == "" {
		return nil
	}
	var segs []onebot.Segment
	for len(content) > 0 {
		start := strings.Index(content, "<")
		if start < 0 {
			segs = append(segs, onebot.Text(content))
			break
		}
		// Find the matching marker; ignore a stray '<' that is not a tag.
		at := strings.Index(content[start:], ">")
		endMark := strings.Index(content, "<qqbot-at-")
		if endMark < 0 || endMark < start {
			if start > 0 {
				segs = append(segs, onebot.Text(content[:start]))
			}
			segs = append(segs, onebot.Text(content[start:]))
			break
		}
		if start > 0 {
			segs = append(segs, onebot.Text(content[:start]))
		}
		_ = at
		closeAt := strings.Index(content[start:], ">")
		if closeAt < 0 {
			segs = append(segs, onebot.Text(content[start:]))
			break
		}
		marker := content[start : start+closeAt+1]
		if body, ok := strings.CutPrefix(marker, "<qqbot-at-"); ok {
			id := strings.TrimSuffix(body, ">")
			segs = append(segs, onebot.At(id))
		} else {
			segs = append(segs, onebot.Text(marker))
		}
		content = content[start+closeAt+1:]
	}
	return segs
}

type qqUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	UnionOpenID  string `json:"union_openid"`
	MemberRole   string `json:"member_role"`
}

type qqAttachment struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	VoiceWavURL string `json:"voice_wav_url"`
}

type qqGroupAtMessage struct {
	ID          string         `json:"id"`
	Content     string         `json:"content"`
	GroupOpenID string         `json:"group_openid"`
	Timestamp   string         `json:"timestamp"`
	Author      qqUser         `json:"author"`
	Attachments []qqAttachment `json:"attachments"`
	MessageType int            `json:"message_type"`
}

type qqC2CMessage struct {
	ID          string         `json:"id"`
	Content     string         `json:"content"`
	Timestamp   string         `json:"timestamp"`
	Author      qqUser         `json:"author"`
	Attachments []qqAttachment `json:"attachments"`
	MessageType int            `json:"message_type"`
}

type qqGroupAdd struct {
	GroupOpenID    string `json:"group_openid"`
	OpMemberOpenID string `json:"op_member_openid"`
	Timestamp      string `json:"timestamp"`
}

func parseTS(s string) int64 {
	if s == "" {
		return time.Now().Unix()
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return time.Now().Unix()
}

func attachmentSegments(atts []qqAttachment) []onebot.Segment {
	var segs []onebot.Segment
	for _, a := range atts {
		switch {
		case strings.HasPrefix(a.ContentType, "image/"):
			segs = append(segs, onebot.Image(a.URL, a.Filename))
		case strings.HasPrefix(a.ContentType, "video/"):
			segs = append(segs, onebot.Video(a.URL, a.Filename))
		case a.ContentType == "voice":
			url := a.VoiceWavURL
			if url == "" {
				url = a.URL
			}
			segs = append(segs, onebot.Record(url, a.Filename))
		default:
			segs = append(segs, onebot.Segment{Type: "file", Data: map[string]any{
				"url": a.URL, "name": a.Filename, "file": a.Filename,
			}})
		}
	}
	return segs
}

// handleQQEvent converts a gateway event into OneBot events and broadcasts it.
func (a *App) handleQQEvent(b *Bot, ev *qq.Event) {
	switch ev.T {
	case "GROUP_AT_MESSAGE_CREATE":
		var d qqGroupAtMessage
		if err := json.Unmarshal(ev.D, &d); err != nil {
			a.Log("warn", "解析群消息失败: "+err.Error())
			return
		}
		group, err := a.st.EnsureGroup(b.rec.ID, d.GroupOpenID, "")
		if err != nil {
			a.Log("error", "记录群信息失败: "+err.Error())
			return
		}
		memberNum, err := a.st.EnsureOpenID(b.rec.ID, "member", d.Author.MemberOpenID)
		if err != nil {
			a.Log("error", "记录群成员失败: "+err.Error())
			return
		}
		segs := append(contentToSegments(d.Content), attachmentSegments(d.Attachments)...)
		if d.MessageType != 0 && len(segs) == 0 {
			segs = []onebot.Segment{onebot.Text("[不支持的消息类型]")}
		}
		raw := onebot.ToCQ(segs)
		b.rememberIncoming("group:"+d.GroupOpenID, d.ID, 1, d.Author.MemberOpenID, group.NumID)
		a.Broadcast(onebot.Event{
			Time:        parseTS(d.Timestamp),
			SelfID:      b.SelfNum(),
			PostType:    "message",
			MessageType: "group",
			SubType:     "normal",
			GroupID:     group.NumID,
			UserID:      memberNum,
			MessageID:   messageIDFromSeq(ev.ID, d.ID),
			Message:     segs,
			RawMessage:  raw,
			MessageSeq:  ev.S,
			Font:        0,
			Sender: onebot.Sender{
				UserID:   memberNum,
				Nickname: d.Author.Username,
				Card:     d.Author.Username,
				Role:     roleName(d.Author.MemberRole),
			},
		})
	case "C2C_MESSAGE_CREATE":
		var d qqC2CMessage
		if err := json.Unmarshal(ev.D, &d); err != nil {
			a.Log("warn", "解析单聊消息失败: "+err.Error())
			return
		}
		userOpenID := d.Author.UserOpenID
		if userOpenID == "" {
			userOpenID = d.Author.ID
		}
		userNum, err := a.st.EnsureOpenID(b.rec.ID, "user", userOpenID)
		if err != nil {
			a.Log("error", "记录用户失败: "+err.Error())
			return
		}
		segs := append(contentToSegments(d.Content), attachmentSegments(d.Attachments)...)
		raw := onebot.ToCQ(segs)
		b.rememberIncoming("private:"+userOpenID, d.ID, 1, userOpenID, 0)
		a.Broadcast(onebot.Event{
			Time:        parseTS(d.Timestamp),
			SelfID:      b.SelfNum(),
			PostType:    "message",
			MessageType: "private",
			SubType:     "friend",
			UserID:      userNum,
			MessageID:   messageIDFromSeq(ev.ID, d.ID),
			Message:     segs,
			RawMessage:  raw,
			MessageSeq:  ev.S,
			Sender: onebot.Sender{
				UserID:   userNum,
				Nickname: d.Author.Username,
				Card:     d.Author.Username,
			},
		})
	case "GROUP_ADD_ROBOT":
		var d qqGroupAdd
		if err := json.Unmarshal(ev.D, &d); err == nil && d.GroupOpenID != "" {
			if g, err := a.st.EnsureGroup(b.rec.ID, d.GroupOpenID, ""); err == nil {
				opNum, _ := a.st.EnsureOpenID(b.rec.ID, "member", d.OpMemberOpenID)
				a.Broadcast(onebot.Event{
					Time:       parseTS(d.Timestamp),
					SelfID:     b.SelfNum(),
					PostType:   "notice",
					NoticeType: "group_increase",
					GroupID:    g.NumID,
					UserID:     b.SelfNum(),
					OperatorID: opNum,
				})
				a.Log("info", fmt.Sprintf("机器人被添加到群 %d", g.NumID))
			}
		}
	case "GROUP_DEL_ROBOT":
		var d qqGroupAdd
		if err := json.Unmarshal(ev.D, &d); err == nil {
			if g, err := a.st.GroupByOpenID(b.rec.ID, d.GroupOpenID); err == nil {
				opNum, _ := a.st.EnsureOpenID(b.rec.ID, "member", d.OpMemberOpenID)
				a.Broadcast(onebot.Event{
					Time:       parseTS(d.Timestamp),
					SelfID:     b.SelfNum(),
					PostType:   "notice",
					NoticeType: "group_decrease",
					SubType:    "kick_me",
					GroupID:    g.NumID,
					UserID:     b.SelfNum(),
					OperatorID: opNum,
				})
				a.Log("info", fmt.Sprintf("机器人被移出群 %d", g.NumID))
			}
		}
	case "C2C_FRIEND_ADD", "FRIEND_ADD":
		a.Log("info", "有用户添加机器人")
	case "C2C_FRIEND_DEL", "FRIEND_DEL":
		a.Log("info", "有用户删除机器人")
	case "GROUP_MSG_REJECT":
		a.Log("info", "群管理关闭了机器人主动消息推送")
	case "GROUP_MSG_RECEIVE":
		a.Log("info", "群管理开启了机器人主动消息推送")
	default:
		// Other intents (guild events) are not mapped; keep a trace in the log.
		if ev.T != "" && !strings.HasPrefix(ev.T, "GUILD_") {
			a.Log("debug", "未映射事件: "+ev.T)
		}
	}
}

func roleName(role string) string {
	switch role {
	case "owner":
		return "owner"
	case "admin":
		return "admin"
	case "member":
		return "member"
	}
	return ""
}

// messageIDFromSeq derives a stable int64 message id.
func messageIDFromSeq(eventID, msgID string) int64 {
	// Use the producer-provided event id when numeric, else hash the msg id.
	for _, r := range eventID {
		if r < '0' || r > '9' {
			return hashID(msgID)
		}
	}
	if eventID != "" {
		if n, err := parseInt64(eventID); err == nil {
			return n
		}
	}
	return hashID(msgID)
}

func hashID(s string) int64 {
	var h int64 = 1469598103934665603
	for _, r := range s {
		h ^= int64(r)
		h *= 1099511628211
	}
	if h < 0 {
		h = -h
	}
	return h % 9000000000000000000
}

var _ store.Group

func (b *Bot) rememberIncoming(key, eventID string, seq int64, sender string, groupNum int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recent[key] = &RecentIncoming{EventID: eventID, Seq: seq, At: time.Now(), Sender: sender, GroupNum: groupNum}
	if len(b.recent) > 2000 {
		for k, v := range b.recent {
			if time.Since(v.At) > 30*time.Minute {
				delete(b.recent, k)
			}
		}
	}
}

func (b *Bot) recentFor(key string) *RecentIncoming {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.recent[key]
}
