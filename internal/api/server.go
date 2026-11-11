// Package api exposes the management console HTTP API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"qqbotonebot/internal/bridge"
	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

type Server struct {
	app *bridge.App
}

func New(app *bridge.App) *Server { return &Server{app: app} }

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/overview", s.overview)
	mux.HandleFunc("/api/bots", s.bots)
	mux.HandleFunc("/api/bots/", s.botByID)
	mux.HandleFunc("/api/listeners", s.listeners)
	mux.HandleFunc("/api/listeners/", s.listenerByID)
	mux.HandleFunc("/api/groups", s.groups)
	mux.HandleFunc("/api/groups/", s.groupByID)
	mux.HandleFunc("/api/test-message", s.testMessage)
	mux.HandleFunc("/api/logs", s.logs)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/ws", s.consoleWS)
	if os.Getenv("QQBOT_TEST_INJECT") == "1" {
		mux.HandleFunc("/api/__test/inject", s.inject)
	}
	mux.HandleFunc("/", s.index)
	return mux
}

func (s *Server) inject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		BotID int64          `json:"bot_id"`
		Type  string         `json:"type"`
		Data  map[string]any `json:"data"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.BotID == 0 {
		req.BotID = 1
	}
	if req.Type == "GROUP_AT_MESSAGE_CREATE" && req.Data == nil {
		req.Data = map[string]any{
			"id":           "INJECT-" + strconv.FormatInt(time.Now().UnixNano(), 10),
			"content":      "注入的测试消息",
			"group_openid": "A1B2C3D4E5F6",
			"timestamp":    time.Now().Format(time.RFC3339),
			"author": map[string]any{
				"id": "MEMBER1", "member_openid": "MEMBER1", "username": "小明", "member_role": "member",
			},
			"message_type": 0,
		}
	}
	if err := s.app.InjectEvent(req.BotID, req.Type, req.Data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

// Overview returns the console summary.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	bots, _ := s.app.Store().ListBots()
	listeners, _ := s.app.Store().ListListeners()
	groups, _ := s.app.Store().ListGroups(0)
	events, actions := s.app.Counters()
	online := 0
	for _, b := range bots {
		if rt := s.app.Bot(b.ID); rt != nil && rt.Status().State == "online" {
			online++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"started_at":   s.app.StartedAtUnix(),
		"uptime":       s.app.Uptime(),
		"bots":         len(bots),
		"bots_online":  online,
		"listeners":    len(listeners),
		"groups":       len(groups),
		"events":       events,
		"actions":      actions,
		"qq_connected": online > 0,
	})
}

// ---- bots ----

type botRequest struct {
	Name    string `json:"name"`
	AppID   string `json:"app_id"`
	Secret  string `json:"secret"`
	Sandbox bool   `json:"sandbox"`
	Intents int64  `json:"intents"`
	Enabled bool   `json:"enabled"`
}

type botView struct {
	store.Bot
	Runtime qq.Status `json:"runtime"`
}

func (s *Server) bots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		bots, err := s.app.Store().ListBots()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]botView, 0, len(bots))
		for _, b := range bots {
			views = append(views, botView{Bot: b, Runtime: s.runtimeStatus(b.ID)})
		}
		writeJSON(w, http.StatusOK, views)
	case http.MethodPost:
		var req botRequest
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		if strings.TrimSpace(req.AppID) == "" || strings.TrimSpace(req.Secret) == "" {
			writeErr(w, http.StatusBadRequest, "AppID 和 AppSecret 不能为空")
			return
		}
		b := store.Bot{Name: req.Name, AppID: req.AppID, Secret: req.Secret, Sandbox: req.Sandbox, Intents: req.Intents, Enabled: req.Enabled}
		if b.Intents == 0 {
			b.Intents = 1 << 25
		}
		if err := s.app.Store().CreateBot(&b); err != nil {
			writeErr(w, http.StatusConflict, "创建失败："+err.Error())
			return
		}
		if b.Enabled {
			_ = s.app.StartBot(b.ID)
		}
		s.app.Log("info", fmt.Sprintf("已添加机器人 %s (AppID %s)", displayName(b.Name, b.AppID), b.AppID))
		writeJSON(w, http.StatusOK, map[string]any{"id": b.ID})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) botByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/bots/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	b, err := s.app.Store().GetBot(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "机器人不存在")
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		writeJSON(w, http.StatusOK, botView{Bot: b, Runtime: s.runtimeStatus(id)})
	case r.Method == http.MethodPut && action == "":
		var req botRequest
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		if strings.TrimSpace(req.AppID) == "" || strings.TrimSpace(req.Secret) == "" {
			writeErr(w, http.StatusBadRequest, "AppID 和 AppSecret 不能为空")
			return
		}
		b.Name, b.AppID, b.Secret, b.Sandbox = req.Name, req.AppID, req.Secret, req.Sandbox
		if req.Intents != 0 {
			b.Intents = req.Intents
		}
		b.Enabled = req.Enabled
		if err := s.app.Store().UpdateBot(&b); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = s.app.RestartBot(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodDelete && action == "":
		s.app.StopBot(id)
		if err := s.app.Store().DeleteBot(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodPost && action == "start":
		if err := s.app.Store().SetBotEnabled(id, true); err != nil {
			writeErr(w, http.StatusNotFound, "机器人不存在")
			return
		}
		if err := s.app.StartBot(id); err != nil {
			writeErr(w, http.StatusBadRequest, "启动失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodPost && action == "stop":
		_ = s.app.Store().SetBotEnabled(id, false)
		s.app.StopBot(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodPost && action == "check":
		id, username, err := s.checkCredentials(b)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "self_id": id, "nickname": username})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) checkCredentials(b store.Bot) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := qq.NewClient(b.AppID, b.Secret, b.Sandbox)
	if _, err := client.Token(ctx); err != nil {
		return "", "", fmt.Errorf("AppID/AppSecret 校验失败：%w", err)
	}
	return client.SelfInfo(ctx)
}

func (s *Server) runtimeStatus(id int64) qq.Status {
	if rt := s.app.Bot(id); rt != nil {
		return rt.Status()
	}
	return qq.Status{State: "stopped"}
}

// ---- listeners ----

type listenerRequest struct {
	BotID   int64  `json:"bot_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Path    string `json:"path"`
	URL     string `json:"url"`
	PostURL string `json:"post_url"`
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
}

func validateListener(l *store.Listener) error {
	switch l.Type {
	case store.ListenerHTTPServer, store.ListenerHTTPSSE, store.ListenerWSServer:
		if l.Port <= 0 || l.Port > 65535 {
			return fmt.Errorf("端口必须在 1-65535 之间")
		}
	case store.ListenerHTTPClient:
		if !strings.HasPrefix(l.URL, "http://") && !strings.HasPrefix(l.URL, "https://") {
			return fmt.Errorf("上报地址必须以 http:// 或 https:// 开头")
		}
	case store.ListenerWSClient:
		if !strings.HasPrefix(l.URL, "ws://") && !strings.HasPrefix(l.URL, "wss://") {
			return fmt.Errorf("连接地址必须以 ws:// 或 wss:// 开头")
		}
	default:
		return fmt.Errorf("未知的监听类型")
	}
	if l.BotID == 0 {
		return fmt.Errorf("请选择所属机器人")
	}
	return nil
}

func (s *Server) listeners(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.app.Store().ListListeners()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if rows == nil {
			rows = []store.Listener{}
		}
		writeJSON(w, http.StatusOK, rows)
	case http.MethodPost:
		var req listenerRequest
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		l := store.Listener{BotID: req.BotID, Name: req.Name, Type: req.Type, Host: req.Host, Port: req.Port, Path: req.Path, URL: req.URL, PostURL: req.PostURL, Token: req.Token, Enabled: req.Enabled}
		if l.Type == store.ListenerHTTPClient || l.Type == store.ListenerWSClient {
			if l.URL == "" && l.PostURL != "" {
				l.URL = l.PostURL
			}
		}
		if err := validateListener(&l); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.app.Store().CreateListener(&l); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if l.Enabled {
			_ = s.app.RestartListeners(l.BotID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": l.ID})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) listenerByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/listeners/"), "/"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	l, err := s.app.Store().GetListener(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "监听不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req listenerRequest
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		oldBot := l.BotID
		l.BotID, l.Name, l.Type, l.Host, l.Port, l.Path, l.URL, l.PostURL, l.Token, l.Enabled =
			req.BotID, req.Name, req.Type, req.Host, req.Port, req.Path, req.URL, req.PostURL, req.Token, req.Enabled
		if err := validateListener(&l); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.app.Store().UpdateListener(&l); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.app.RestartListeners(oldBot)
		if l.BotID != oldBot {
			_ = s.app.RestartListeners(l.BotID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		_ = s.app.Store().DeleteListener(id)
		_ = s.app.RestartListeners(l.BotID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---- groups ----

type groupView struct {
	store.Group
	BotName string `json:"bot_name"`
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		botID, _ := strconv.ParseInt(r.URL.Query().Get("bot_id"), 10, 64)
		rows, err := s.app.Store().ListGroups(botID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		names := map[int64]string{}
		bots, _ := s.app.Store().ListBots()
		for _, b := range bots {
			names[b.ID] = displayName(b.Name, b.AppID)
		}
		views := make([]groupView, 0, len(rows))
		for _, g := range rows {
			views = append(views, groupView{Group: g, BotName: names[g.BotID]})
		}
		writeJSON(w, http.StatusOK, views)
	case http.MethodPost:
		var req struct {
			BotID  int64  `json:"bot_id"`
			OpenID string `json:"open_id"`
			Name   string `json:"name"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		if req.BotID == 0 || strings.TrimSpace(req.OpenID) == "" {
			writeErr(w, http.StatusBadRequest, "需要机器人 ID 和群 openid")
			return
		}
		g, err := s.app.Store().EnsureGroup(req.BotID, req.OpenID, req.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, g)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) groupByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		if err := s.app.Store().SetGroupName(id, req.Name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.app.Store().DeleteGroup(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---- test message ----

type testRequest struct {
	BotID    int64  `json:"bot_id"`
	GroupNum int64  `json:"group_id"`
	OpenID   string `json:"group_openid"`
	Message  string `json:"message"`
	Markdown bool   `json:"markdown"`
}

// testMessage sends an active (non-reply) message, which is what the console
// needs to prove credentials and delivery work.
func (s *Server) testMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req testRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	rt := s.app.Bot(req.BotID)
	if rt == nil {
		writeErr(w, http.StatusBadRequest, "机器人未启动：请先在机器人列表点击启动")
		return
	}
	openID := strings.TrimSpace(req.OpenID)
	if openID == "" && req.GroupNum != 0 {
		g, err := s.app.Store().GroupByNum(req.BotID, req.GroupNum)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("找不到群 %d 的 openid，请手动填写", req.GroupNum))
			return
		}
		openID = g.OpenID
	}
	if openID == "" {
		writeErr(w, http.StatusBadRequest, "请提供群 openid")
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeErr(w, http.StatusBadRequest, "消息内容不能为空")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := rt.SendTestGroup(ctx, openID, msg, req.Markdown)
	if err != nil {
		s.app.Log("error", "测试消息发送失败: "+err.Error())
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.app.Log("info", fmt.Sprintf("测试消息已发送到群 %s (消息 ID %s)", openID, res.ID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_id": res.ID})
}

// ---- logs / events / console websocket ----

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	entries := s.app.Logs()
	if entries == nil {
		entries = []bridge.LogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	events := s.app.RecentEvents()
	if events == nil {
		events = []bridge.ConsoleEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

func displayName(name, appID string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return "机器人 " + appID
}

var _ = onebot.OK
