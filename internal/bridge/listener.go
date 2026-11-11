package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

// listenerRuntime is one configured OneBot transport attached to a bot.
type listenerRuntime struct {
	cfg    store.Listener
	bot    *Bot
	app    *App
	server *http.Server
	ln     net.Listener

	mu      sync.Mutex
	started bool
	stopped bool
	lastErr string
	// client-side event queue for HTTP client mode
	queue  chan []byte
	stopCh chan struct{}
	// reverse ws connections
	conns      map[*websocket.Conn]string // conn -> role
	sses       map[int]chan []byte
	pollQueues map[chan []byte]struct{}
	sseID      int

	connMu sync.Mutex
	active bool
	up     bool
}

func (a *App) startListener(b *Bot, cfg store.Listener) (*listenerRuntime, error) {
	rt := &listenerRuntime{
		cfg:        cfg,
		bot:        b,
		app:        a,
		stopCh:     make(chan struct{}),
		conns:      map[*websocket.Conn]string{},
		sses:       map[int]chan []byte{},
		pollQueues: map[chan []byte]struct{}{},
	}
	switch cfg.Type {
	case store.ListenerHTTPServer, store.ListenerHTTPSSE, store.ListenerWSServer:
		if err := rt.startServer(); err != nil {
			return nil, err
		}
		rt.up = true
	case store.ListenerHTTPClient:
		rt.queue = make(chan []byte, 1024)
		rt.up = true
		go rt.httpClientLoop()
	case store.ListenerWSClient:
		rt.up = true
		go rt.reverseWSLoop()
	default:
		return nil, fmt.Errorf("未知监听类型: %s", cfg.Type)
	}
	rt.started = true
	a.publishJSON(map[string]any{"type": "listener", "id": cfg.ID, "state": "running"})
	return rt, nil
}

func (rt *listenerRuntime) stop() {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return
	}
	rt.stopped = true
	rt.mu.Unlock()
	close(rt.stopCh)
	if rt.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = rt.server.Shutdown(ctx)
		cancel()
	}
	rt.connMu.Lock()
	for c := range rt.conns {
		c.Close()
	}
	rt.conns = map[*websocket.Conn]string{}
	rt.connMu.Unlock()
	rt.mu.Lock()
	for _, ch := range rt.sses {
		close(ch)
	}
	rt.sses = map[int]chan []byte{}
	rt.mu.Unlock()
	_ = rt.ln
	rt.app.publishJSON(map[string]any{"type": "listener", "id": rt.cfg.ID, "state": "stopped"})
}

func (rt *listenerRuntime) stoppedCh() bool {
	select {
	case <-rt.stopCh:
		return true
	default:
		return false
	}
}

// route resolves this listener's configured path.
func (rt *listenerRuntime) route(suffix string) string {
	p := strings.TrimSpace(rt.cfg.Path)
	if p == "" {
		return suffix
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if suffix == "/" {
		return p
	}
	return strings.TrimSuffix(p, "/") + suffix
}

func (rt *listenerRuntime) addr() (string, error) {
	host := strings.TrimSpace(rt.cfg.Host)
	if host == "" {
		host = "0.0.0.0"
	}
	if rt.cfg.Port <= 0 || rt.cfg.Port > 65535 {
		return "", fmt.Errorf("端口无效: %d", rt.cfg.Port)
	}
	return net.JoinHostPort(host, strconv.Itoa(rt.cfg.Port)), nil
}

func (rt *listenerRuntime) startServer() error {
	addr, err := rt.addr()
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	switch rt.cfg.Type {
	case store.ListenerHTTPServer, store.ListenerHTTPSSE:
		mux.HandleFunc(rt.route("/"), func(w http.ResponseWriter, r *http.Request) {
			rt.handleHTTP(w, r)
		})
		if rt.cfg.Type == store.ListenerHTTPSSE {
			mux.HandleFunc(rt.route("/_events"), rt.handleSSE)
			mux.HandleFunc(rt.route("/events"), rt.handleSSE)
		}
	case store.ListenerWSServer:
		upgrader := websocket.Upgrader{
			CheckOrigin:  func(*http.Request) bool { return true },
			Subprotocols: []string{"onebot.v11"},
		}
		handler := func(w http.ResponseWriter, r *http.Request) {
			rt.handleWSServer(w, r, &upgrader)
		}
		mux.HandleFunc(rt.route("/"), handler)
		mux.HandleFunc(rt.route("/api"), handler)
		mux.HandleFunc(rt.route("/event"), handler)
		mux.HandleFunc(rt.route("/universal"), handler)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	rt.ln = ln
	rt.server = &http.Server{Handler: mux}
	go func() {
		if err := rt.server.Serve(ln); err != nil && !strings.Contains(err.Error(), "closed") {
			if !rt.stoppedCh() {
				slog.Error("listener server", "id", rt.cfg.ID, "err", err)
				rt.app.publishJSON(map[string]any{"type": "listener", "id": rt.cfg.ID, "state": "error", "error": err.Error()})
			}
		}
	}()
	rt.app.Log("info", fmt.Sprintf("监听 %s 已启动于 %s", rt.cfg.Name, addr))
	return nil
}

// ---- auth ----

func (rt *listenerRuntime) authorized(r *http.Request) bool {
	if rt.cfg.Token == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.EqualFold(auth, "Bearer "+rt.cfg.Token) || strings.EqualFold(auth, "Token "+rt.cfg.Token) || auth == rt.cfg.Token {
		return true
	}
	if q := r.URL.Query().Get("access_token"); q != "" && q == rt.cfg.Token {
		return true
	}
	return false
}

func (rt *listenerRuntime) unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(onebot.Failed(nil, 403, "token 验证失败"))
}

// ---- HTTP server & SSE ----

func (rt *listenerRuntime) handleHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	w.Header().Set("Content-Type", "application/json")
	if !rt.authorized(r) {
		rt.unauthorized(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rt.handleHTTPEvents(w, r) // long-polling / simple event endpoint
		return
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(onebot.Failed(nil, 400, "读取请求体失败"))
		return
	}
	var act onebot.Action
	if err := json.Unmarshal(raw, &act); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(onebot.Failed(nil, 400, "无效的 JSON: "+err.Error()))
		return
	}
	if act.Action == "get_status" && act.Echo != nil {
		// normal action
	}
	if act.Action == "" {
		// Some clients POST events to the API endpoint by mistake; accept silently.
		_ = json.NewEncoder(w).Encode(onebot.OK(nil, nil))
		return
	}
	resp := rt.app.HandleAction(rt.bot.SelfNum(), act)
	rt.app.Log("debug", "API "+act.Action+": "+map[bool]string{true: "ok", false: "failed"}[resp.Status == "ok"])
	_ = json.NewEncoder(w).Encode(resp)
}

func (rt *listenerRuntime) handleHTTPEvents(w http.ResponseWriter, r *http.Request) {
	// Some clients like to pull events with a GET. Keep a per-request queue and
	// hand back whatever is queued, waiting briefly for the next event.
	ch := make(chan []byte, 64)
	rt.mu.Lock()
	rt.pollQueues[ch] = struct{}{}
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		delete(rt.pollQueues, ch)
		rt.mu.Unlock()
	}()
	select {
	case data := <-ch:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	case <-time.After(15 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	case <-rt.stopCh:
	}
}

// handleSSE streams events as text/event-stream.
func (rt *listenerRuntime) handleSSE(w http.ResponseWriter, r *http.Request) {
	if !rt.authorized(r) {
		rt.unauthorized(w)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "event: ready\ndata: {\"post_type\":\"meta_event\",\"meta_event_type\":\"lifecycle\",\"self_id\":%d,\"time\":%d}\n\n", rt.bot.SelfNum(), time.Now().Unix())
	fl.Flush()

	ch := make(chan []byte, 256)
	rt.mu.Lock()
	rt.sseID++
	id := rt.sseID
	rt.sses[id] = ch
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		delete(rt.sses, id)
		rt.mu.Unlock()
	}()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	rt.app.Log("info", fmt.Sprintf("SSE 客户端接入 (%s)", r.RemoteAddr))
	for {
		select {
		case <-r.Context().Done():
			return
		case <-rt.stopCh:
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			fl.Flush()
		}
	}
}

// handleHTTPEvents for GET on the main path: serve a single flush of events
// (OneBot HTTP API does not define GET semantics, so return 204 politely).
func (rt *listenerRuntime) sendEvent(data []byte) {
	// fan out depending on transport
	switch rt.cfg.Type {
	case store.ListenerHTTPSSE:
		rt.mu.Lock()
		for _, ch := range rt.sses {
			select {
			case ch <- data:
			default:
			}
		}
		rt.mu.Unlock()
	case store.ListenerHTTPServer:
		rt.mu.Lock()
		queues := make([]chan []byte, 0, len(rt.pollQueues))
		for ch := range rt.pollQueues {
			queues = append(queues, ch)
		}
		rt.mu.Unlock()
		for _, ch := range queues {
			select {
			case ch <- data:
			default:
			}
		}
	case store.ListenerWSServer, store.ListenerWSClient:
		data2 := data
		rt.connMu.Lock()
		conns := make([]*websocket.Conn, 0, len(rt.conns))
		for c, role := range rt.conns {
			if role == "event" || role == "universal" {
				conns = append(conns, c)
			}
		}
		rt.connMu.Unlock()
		for _, c := range conns {
			rt.writeWS(c, data2)
		}
	case store.ListenerHTTPClient:
		if rt.queue != nil {
			select {
			case rt.queue <- data:
			case <-rt.stopCh:
			default:
				rt.app.Log("warn", fmt.Sprintf("监听 %s 事件队列已满，丢弃一条事件", rt.cfg.Name))
			}
		}
	}
}

// ---- websocket helpers ----

func (rt *listenerRuntime) writeWS(c *websocket.Conn, data []byte) {
	rt.connMu.Lock()
	defer rt.connMu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		_ = c.Close()
	}
}

func (rt *listenerRuntime) handleWSServer(w http.ResponseWriter, r *http.Request, up *websocket.Upgrader) {
	if !rt.authorized(r) {
		rt.unauthorized(w)
		return
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	role := strings.ToLower(r.Header.Get("X-Client-Role"))
	switch role {
	case "api", "event", "universal":
	default:
		role = "universal"
	}
	rt.connMu.Lock()
	rt.conns[conn] = role
	rt.connMu.Unlock()
	defer func() {
		rt.connMu.Lock()
		delete(rt.conns, conn)
		rt.connMu.Unlock()
		conn.Close()
	}()
	rt.app.Log("info", fmt.Sprintf("WebSocket 客户端接入 (%s, role=%s)", r.RemoteAddr, role))

	// Send lifecycle meta event to event/universal clients.
	if role == "event" || role == "universal" {
		meta := map[string]any{
			"post_type": "meta_event", "meta_event_type": "lifecycle", "sub_type": "connect",
			"self_id": rt.bot.SelfNum(), "time": time.Now().Unix(),
		}
		b, _ := json.Marshal(meta)
		rt.writeWS(conn, b)
	}

	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			return
		}
		if role == "event" {
			continue
		}
		var act onebot.Action
		if err := json.Unmarshal(raw, &act); err != nil {
			resp, _ := json.Marshal(onebot.Failed(nil, 400, "无效的 JSON"))
			rt.writeWS(conn, resp)
			continue
		}
		resp := rt.app.HandleAction(rt.bot.SelfNum(), act)
		out, _ := json.Marshal(resp)
		rt.writeWS(conn, out)
	}
}

// ---- HTTP client mode (events POSTed to a configured URL) ----

func (rt *listenerRuntime) httpClientLoop() {
	hc := &http.Client{Timeout: 15 * time.Second}
	target := strings.TrimSpace(rt.cfg.URL)
	if target == "" {
		rt.app.Log("error", fmt.Sprintf("监听 %s 未填写上报地址", rt.cfg.Name))
		return
	}
	rt.app.Log("info", fmt.Sprintf("HTTP 客户端上报已启动 -> %s", target))
	for {
		select {
		case <-rt.stopCh:
			return
		case data := <-rt.queue:
			go func(data []byte) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				// OneBot allows the whole event to be the body; we wrap in one
				// array element too, since many SDKs accept both.
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				if rt.cfg.Token != "" {
					req.Header.Set("Authorization", "Token "+rt.cfg.Token)
				}
				req.Header.Set("X-Self-ID", strconv.FormatInt(rt.bot.SelfNum(), 10))
				resp, err := hc.Do(req)
				if err != nil {
					rt.app.Log("warn", fmt.Sprintf("上报事件到 %s 失败: %v", target, err))
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}(data)
		}
	}
}

// ---- reverse WebSocket client mode ----

func (rt *listenerRuntime) reverseWSLoop() {
	target := strings.TrimSpace(rt.cfg.URL)
	if target == "" {
		rt.app.Log("error", fmt.Sprintf("监听 %s 未填写连接地址", rt.cfg.Name))
		return
	}
	attempt := 0
	for {
		if rt.stoppedCh() {
			return
		}
		err := rt.reverseWSOnce(target)
		if rt.stoppedCh() {
			return
		}
		attempt++
		wait := time.Duration(attempt*3) * time.Second
		if wait > time.Minute {
			wait = time.Minute
		}
		if err != nil {
			rt.app.Log("warn", fmt.Sprintf("反向 WS 连接 %s 断开: %v（%s 后重连）", target, err, wait))
		}
		select {
		case <-rt.stopCh:
			return
		case <-time.After(wait):
		}
	}
}

func (rt *listenerRuntime) reverseWSOnce(target string) error {
	dialer := *websocket.DefaultDialer
	if rt.cfg.Token != "" {
		dialer.Subprotocols = []string{"onebot.v11"}
	}
	header := http.Header{}
	header.Set("X-Self-ID", strconv.FormatInt(rt.bot.SelfNum(), 10))
	header.Set("X-Client-Role", "Universal")
	header.Set("User-Agent", "OneBot/11 qqbot-onebot")
	if rt.cfg.Token != "" {
		header.Set("Authorization", "Token "+rt.cfg.Token)
	}
	conn, _, err := dialer.Dial(target, header)
	if err != nil {
		return err
	}
	defer conn.Close()

	rt.connMu.Lock()
	rt.conns[conn] = "universal"
	rt.connMu.Unlock()
	defer func() {
		rt.connMu.Lock()
		delete(rt.conns, conn)
		rt.connMu.Unlock()
	}()
	rt.app.Log("info", fmt.Sprintf("反向 WS 已连接 %s", target))

	// OneBot reverse clients expect a lifecycle meta event right away.
	meta := map[string]any{
		"post_type": "meta_event", "meta_event_type": "lifecycle", "sub_type": "connect",
		"self_id": rt.bot.SelfNum(), "time": time.Now().Unix(),
	}
	mb, _ := json.Marshal(meta)
	rt.writeWS(conn, mb)

	var closeOnce sync.Once
	done := make(chan struct{})
	go func() {
		select {
		case <-rt.stopCh:
			closeOnce.Do(func() { conn.Close() })
		case <-done:
		}
	}()
	defer close(done)

	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			return err
		}
		var act onebot.Action
		if err := json.Unmarshal(raw, &act); err != nil {
			resp, _ := json.Marshal(onebot.Failed(nil, 400, "无效的 JSON"))
			rt.writeWS(conn, resp)
			continue
		}
		resp := rt.app.HandleAction(rt.bot.SelfNum(), act)
		out, _ := json.Marshal(resp)
		rt.writeWS(conn, out)
	}
}

var _ = qq.APIError{}
