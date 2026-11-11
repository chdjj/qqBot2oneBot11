package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

// HandleAction executes one OneBot API call for the bot identified by selfID.
func (a *App) HandleAction(selfID int64, act onebot.Action) onebot.APIResponse {
	a.counters.actions.Add(1)
	b := a.botForSelf(selfID)
	if b == nil {
		return onebot.Failed(act.Echo, 100, "没有可用的机器人，请在控制台启用并配置 AppID/AppSecret")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p := act.Params
	if p == nil {
		p = map[string]any{}
	}
	switch act.Action {
	case "send_msg":
		mt := onebot.StrParam(p, "message_type")
		if mt == "" {
			if onebot.IntParam(p, "user_id") != 0 && onebot.IntParam(p, "group_id") == 0 {
				mt = "private"
			} else {
				mt = "group"
			}
		}
		if mt == "group" {
			return a.sendGroup(ctx, b, act.Echo, p)
		}
		return a.sendPrivate(ctx, b, act.Echo, p)
	case "send_group_msg":
		return a.sendGroup(ctx, b, act.Echo, p)
	case "send_private_msg":
		return a.sendPrivate(ctx, b, act.Echo, p)
	case "delete_msg":
		return a.deleteMsg(ctx, b, act.Echo, p)
	case "get_msg":
		return onebot.OK(act.Echo, map[string]any{"message_id": onebot.IntParam(p, "message_id")})
	case "get_login_info":
		return onebot.OK(act.Echo, map[string]any{"user_id": b.SelfNum(), "nickname": b.Status().Username})
	case "get_status":
		st := b.Status()
		online := st.State == "online"
		return onebot.OK(act.Echo, map[string]any{
			"online": online, "good": online,
			"state": st.State, "message": st.Message,
			"connected_at": st.ConnectedAt,
		})
	case "get_version_info":
		return onebot.OK(act.Echo, map[string]any{"app_name": "qqbot-onebot", "app_version": "1.0.0", "protocol": "qqbot-v2"})
	case "get_group_info":
		return a.groupInfo(ctx, b, act.Echo, p)
	case "get_group_list":
		return a.groupList(b, act.Echo)
	case "get_group_member_info":
		return onebot.OK(act.Echo, map[string]any{"group_id": onebot.IntParam(p, "group_id"), "user_id": onebot.IntParam(p, "user_id")})
	case "get_group_member_list":
		return onebot.OK(act.Echo, []map[string]any{})
	case "can_send_image", "can_send_record":
		return onebot.OK(act.Echo, map[string]any{"yes": true})
	case "get_image":
		return onebot.OK(act.Echo, map[string]any{"file": onebot.StrParam(p, "file")})
	case "set_friend_add_request", "set_group_add_request", "set_restart":
		return onebot.OK(act.Echo, nil)
	default:
		return onebot.Failed(act.Echo, 1404, "不支持的接口: "+act.Action)
	}
}

func (a *App) botForSelf(selfID int64) *Bot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if selfID != 0 {
		for _, b := range a.bots {
			if b.SelfNum() == selfID {
				return b
			}
		}
	}
	var found *Bot
	for _, b := range a.bots {
		if found == nil {
			found = b
			continue
		}
		if found.Status().State != "online" && b.Status().State == "online" {
			found = b
		}
	}
	return found
}

func (a *App) sendGroup(ctx context.Context, b *Bot, echo any, p map[string]any) onebot.APIResponse {
	groupNum := onebot.IntParam(p, "group_id")
	g, err := a.st.GroupByNum(b.rec.ID, groupNum)
	if err != nil {
		if s := strings.TrimSpace(onebot.StrParam(p, "group_openid")); s != "" {
			g, err = a.st.EnsureGroup(b.rec.ID, s, "")
		}
	}
	if err != nil {
		return onebot.Failed(echo, 120, fmt.Sprintf("未知群 %d：该群 openid 需要先让机器人在该群收到一条 @ 消息才能获得，请先在群里 @ 机器人发一条消息", groupNum))
	}
	segs, err := onebot.ParseSegments(p["message"])
	if err != nil {
		return onebot.Failed(echo, 400, "消息格式错误: "+err.Error())
	}
	return a.dispatchMessage(ctx, b, echo, "group", g.OpenID, g.NumID, segs, onebot.IntParam(p, "msg_seq"))
}

func (a *App) sendPrivate(ctx context.Context, b *Bot, echo any, p map[string]any) onebot.APIResponse {
	userNum := onebot.IntParam(p, "user_id")
	openID, err := a.st.OpenIDByNum(b.rec.ID, "user", userNum)
	if err != nil {
		if s := strings.TrimSpace(onebot.StrParam(p, "user_openid")); s != "" {
			openID = s
			_, _ = a.st.EnsureOpenID(b.rec.ID, "user", s)
		} else {
			return onebot.Failed(echo, 120, fmt.Sprintf("未知用户 %d：需要该用户先给机器人发过单聊消息", userNum))
		}
	}
	segs, err := onebot.ParseSegments(p["message"])
	if err != nil {
		return onebot.Failed(echo, 400, "消息格式错误: "+err.Error())
	}
	return a.dispatchMessage(ctx, b, echo, "private", openID, userNum, segs, onebot.IntParam(p, "msg_seq"))
}

// dispatchMessage converts OneBot segments to a QQ message and sends it.
func (a *App) dispatchMessage(ctx context.Context, b *Bot, echo any, kind, openID string, targetNum int64, segs []onebot.Segment, msgSeq int64) onebot.APIResponse {
	if len(segs) == 0 {
		return onebot.Failed(echo, 400, "消息为空")
	}
	msg, err := segmentsToQQMessage(kind, segs)
	if err != nil {
		return onebot.Failed(echo, 400, err.Error())
	}
	if msg.msg.MsgType == 7 {
		// Media must be registered first, which requires the network call.
		var up *qq.UploadResult
		if kind == "group" {
			up, err = b.client.UploadGroupMedia(ctx, openID, msg.mediaFileType, msg.mediaURL)
		} else {
			up, err = b.client.UploadC2CMedia(ctx, openID, msg.mediaFileType, msg.mediaURL)
		}
		if err != nil {
			return onebot.Failed(echo, qqRetCode(err), "上传媒体失败: "+qqErrorMessage(err))
		}
		msg.msg.Media = &qq.Media{FileInfo: up.FileInfo}
	}

	// Passive reply if this chat just received a message: QQ needs msg_id for
	// replies and passive replies are exempt from the active-message quota.
	if msgSeq == 0 {
		msgSeq = 1
	}
	passive := false
	if ri := b.recentFor(kind + ":" + openID); ri != nil && time.Since(ri.At) < 4*time.Minute && ri.EventID != "" {
		msg.msg.MsgID = ri.EventID
		seq := int(msgSeq)
		msg.msg.MsgSeq = &seq
		passive = true
	}

	// QQ caps plain text at 4000 characters. Split long text and send the
	// remainder as follow-ups; the last chunk's id is the one returned.
	chunks := splitText(msg.msg.Content, maxTextLen)
	if len(chunks) > 1 && msg.msg.MsgType == 0 {
		msg.msg.Content = chunks[0]
	}
	var res *qq.SendResult
	var lastErr error
	for i := 0; ; i++ {
		if i > 0 && msg.msg.MsgType == 0 {
			msg.msg.Content = chunks[i]
			// Only the first chunk can share the passive reply slot; the rest
			// are ordinary messages (seq increments so QQ does not reject them).
			if passive && msg.msg.MsgSeq != nil {
				n := *msg.msg.MsgSeq + i
				msg.msg.MsgSeq = &n
			}
		}
		if kind == "group" {
			res, lastErr = b.client.SendGroup(ctx, openID, &msg.msg)
		} else {
			res, lastErr = b.client.SendC2C(ctx, openID, &msg.msg)
		}
		if lastErr != nil {
			break
		}
		if i+1 >= len(chunks) || msg.msg.MsgType != 0 {
			break
		}
	}
	if lastErr != nil {
		return onebot.Failed(echo, qqRetCode(lastErr), qqErrorMessage(lastErr))
	}

	qqMsgID, _ := parseInt64(res.ID)
	if qqMsgID == 0 {
		qqMsgID = hashID(res.ID + fmt.Sprint(time.Now().UnixNano()))
	}
	b.mu.Lock()
	b.sent[qqMsgID] = &SentMessage{Kind: kind, OpenID: openID, QQID: res.ID, At: time.Now()}
	if len(b.sent) > 5000 {
		for k, v := range b.sent {
			if time.Since(v.At) > time.Hour {
				delete(b.sent, k)
			}
		}
	}
	b.mu.Unlock()
	a.Log("info", fmt.Sprintf("%s 消息已发送 -> %s: %s", map[string]string{"group": "群", "private": "单聊"}[kind], openID, previewSegments(segs)))
	return onebot.OK(echo, map[string]any{"message_id": qqMsgID})
}

// maxTextLen is the QQ v2 plain-text limit for one message.
const maxTextLen = 4000

// splitText breaks s into chunks of at most limit runes, preferring to break
// after a newline so replies stay readable.
func splitText(s string, limit int) []string {
	if limit <= 0 || len([]rune(s)) <= limit {
		return []string{s}
	}
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		n := limit
		if len(runes) < n {
			n = len(runes)
		}
		cut := n
		for i := n; i > n/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	return out
}

// SendTestGroup sends an active (non-reply) message for the console's test
// button, so delivery works even when no recent inbound message exists.
func (b *Bot) SendTestGroup(ctx context.Context, openID, content string, markdown bool) (*qq.SendResult, error) {
	m := &qq.Message{MsgType: 0, Content: content}
	if markdown {
		m.Content = ""
		m.MsgType = 2
		m.Markdown = map[string]any{
			"content": content,
		}
	}
	return b.client.SendGroup(ctx, openID, m)
}

func previewSegments(segs []onebot.Segment) string {
	s := onebot.PlainText(segs)
	if len([]rune(s)) > 60 {
		return string([]rune(s)[:60]) + "…"
	}
	return s
}

// Client exposes the underlying QQ API client for the console.
func (b *Bot) Client() *qq.Client { return b.client }

func (a *App) deleteMsg(ctx context.Context, b *Bot, echo any, p map[string]any) onebot.APIResponse {
	msgID := onebot.IntParam(p, "message_id")
	b.mu.Lock()
	m := b.sent[msgID]
	b.mu.Unlock()
	if m == nil {
		return onebot.Failed(echo, 120, "找不到该消息（只能撤回由本适配器发送的消息）")
	}
	// QQ only allows retracting messages sent within the last 2 minutes.
	if time.Since(m.At) > 110*time.Second {
		return onebot.Failed(echo, 40064004, "已超出消息撤回时限（2 分钟）")
	}
	var err error
	if m.Kind == "group" {
		err = b.client.RetractGroupMessage(ctx, m.OpenID, m.QQID)
	} else {
		err = b.client.RetractC2CMessage(ctx, m.OpenID, m.QQID)
	}
	if err != nil {
		return onebot.Failed(echo, qqRetCode(err), qqErrorMessage(err))
	}
	return onebot.OK(echo, nil)
}

func (a *App) groupInfo(ctx context.Context, b *Bot, echo any, p map[string]any) onebot.APIResponse {
	groupNum := onebot.IntParam(p, "group_id")
	g, err := a.st.GroupByNum(b.rec.ID, groupNum)
	if err != nil {
		return onebot.Failed(echo, 120, "未知群")
	}
	info := map[string]any{"group_id": g.NumID, "group_name": g.Name, "group_openid": g.OpenID, "member_count": 0}
	if raw, err := b.client.GroupInfo(ctx, g.OpenID); err == nil {
		if name, _ := raw["group_name"].(string); name != "" {
			info["group_name"] = name
			_ = a.st.SetGroupName(g.ID, name)
		}
		if n, ok := raw["group_member_num"].(float64); ok {
			info["member_count"] = int64(n)
		}
	}
	return onebot.OK(echo, info)
}

func (a *App) groupList(b *Bot, echo any) onebot.APIResponse {
	rows, err := a.st.ListGroups(b.rec.ID)
	if err != nil {
		return onebot.Failed(echo, 500, err.Error())
	}
	out := make([]map[string]any, 0, len(rows))
	for _, g := range rows {
		out = append(out, map[string]any{"group_id": g.NumID, "group_name": g.Name, "group_openid": g.OpenID})
	}
	return onebot.OK(echo, out)
}

// qqConvertedMessage carries the pieces needed to finish building a send.
type qqConvertedMessage struct {
	msg           qq.Message
	mediaFileType int
	mediaURL      string
}

// segmentsToQQMessage maps OneBot segments onto a QQ v2 message payload.
func segmentsToQQMessage(kind string, segs []onebot.Segment) (qqConvertedMessage, error) {
	var textParts []string
	var media *qqConvertedMessage
	for _, seg := range segs {
		switch seg.Type {
		case "text":
			t, _ := seg.Data["text"].(string)
			textParts = append(textParts, t)
		case "at":
			qq := onebot.StrParam(seg.Data, "qq")
			if qq == "all" {
				return qqConvertedMessage{}, errors.New("QQ 官方机器人不支持 @全体成员")
			}
			nick := onebot.StrParam(seg.Data, "name")
			if nick == "" {
				nick = qq
			}
			textParts = append(textParts, "@"+nick+" ")
		case "image", "record", "video", "file":
			if media != nil {
				return qqConvertedMessage{}, errors.New("QQ 官方机器人一次只能发送一个图片/语音/视频")
			}
			url := onebot.StrParam(seg.Data, "url")
			file := onebot.StrParam(seg.Data, "file")
			if url == "" && strings.HasPrefix(file, "http") {
				url = file
			}
			if url == "" {
				return qqConvertedMessage{}, errors.New("QQ 官方机器人发送媒体需要提供可访问的 url")
			}
			ft := 1
			switch seg.Type {
			case "record":
				ft = 3
			case "video":
				ft = 2
			case "file":
				ft = 4
			}
			media = &qqConvertedMessage{mediaFileType: ft, mediaURL: url}
		case "reply":
			// Quoted replies go through msg_id; the marker itself is ignored.
		case "face":
			// QQ v2 plain-text sends have no face encoding.
		case "markdown":
			// QQ v2 has a dedicated Markdown message type. A message may only
			// be Markdown or text, not both.
			content := onebot.StrParam(seg.Data, "content")
			if content == "" {
				content = onebot.StrParam(seg.Data, "data")
			}
			if content == "" {
				return qqConvertedMessage{}, errors.New("markdown 段缺少 content")
			}
			return qqConvertedMessage{msg: qq.Message{MsgType: 2, Markdown: map[string]any{"content": content}}}, nil
		case "json", "xml", "share", "music":
			return qqConvertedMessage{}, errors.New("QQ 官方机器人不支持该消息段: " + seg.Type)
		default:
			return qqConvertedMessage{}, errors.New("不支持的消息段: " + seg.Type)
		}
	}
	content := strings.TrimSpace(strings.Join(textParts, ""))
	if media != nil {
		if content == "" {
			content = " "
		}
		return qqConvertedMessage{
			msg:           qq.Message{MsgType: 7, Content: content},
			mediaFileType: media.mediaFileType, mediaURL: media.mediaURL,
		}, nil
	}
	if content == "" {
		return qqConvertedMessage{}, errors.New("消息内容为空")
	}
	return qqConvertedMessage{msg: qq.Message{MsgType: 0, Content: content}}, nil
}

func qqErrorMessage(err error) string {
	var ae *qq.APIError
	if errors.As(err, &ae) {
		msg := ae.Message
		if msg == "" {
			msg = ae.Error()
		}
		return qqFriendlyMessage(ae, msg)
	}
	return err.Error()
}

func qqFriendlyMessage(ae *qq.APIError, msg string) string {
	code := ae.Code
	if code == 0 {
		code = ae.ErrCode
	}
	switch code {
	case 40034102:
		return fmt.Sprintf("%s（错误码 40034102）：主动消息无权限，请让群/用户先与机器人互动，或等待频控解除", msg)
	case 11244:
		return fmt.Sprintf("%s（错误码 11244）：消息内容审核未通过", msg)
	case 304003:
		return fmt.Sprintf("%s（错误码 304003）：openid 无效或不属于该机器人", msg)
	case 304023:
		return fmt.Sprintf("%s（错误码 304023）：机器人被禁言", msg)
	case 11253:
		return fmt.Sprintf("%s（错误码 11253）：应用无该接口的访问权限", msg)
	}
	return fmt.Sprintf("%s（错误码 %d）", msg, code)
}

func qqRetCode(err error) int {
	var ae *qq.APIError
	if errors.As(err, &ae) {
		if ae.Code != 0 {
			return ae.Code
		}
		return ae.Status
	}
	return 500
}

var _ store.Group
