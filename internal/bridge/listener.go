package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
			// A plain HTTP POST to a WS port is almost always a misconfiguration;
			// answer with the OneBot action anyway so it can be debugged with curl.
			if !websocket.IsWebSocketUpgrade(r) {
				rt.handleHTTP(w, r)
				return
			}
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

// unauthorized always logs, even without the debug switch: a rejected request
// is exactly what someone is looking for when their client "gets no response".
func (rt *listenerRuntime) unauthorized(w http.ResponseWriter, r *http.Request) {
	presented := r.Header.Get("Authorization")
	if presented == "" {
		presented = r.Header.Get("X-Admin-Token")
	}
	if presented == "" && r.URL.Query().Get("access_token") != "" {
		presented = "?access_token=" + r.URL.Query().Get("access_token")
	}
	hint := "(未携带 token)"
	if presented != "" {
		hint = "携带了 " + redactToken(strings.TrimPrefix(strings.TrimPrefix(presented, "Bearer "), "Token "))
	}
	rt.app.Log("warn", fmt.Sprintf(
		"[%s/#%d %s] ✕ 拒绝未授权请求 %s %s ← %s，期望的 token 是 %s",
		rt.cfg.Name, rt.cfg.ID, transportName(rt.cfg.Type), r.Method, r.URL.Path,
		hint, redactToken(rt.cfg.Token)))
	w.Header().Set("Content-Type", "application/json")
	// Follow the spec: 401 when no access token was supplied, 403 when one was
	// supplied but is wrong.
	status := http.StatusForbidden
	if presented == "" {
		status = http.StatusUnauthorized
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(onebot.Failed(nil, statusCodeToRetCode(status), "token 验证失败"))
}

// statusCodeToRetCode maps an HTTP status onto the WebSocket retcode scale that
// OneBot uses for the same condition (1400/1401/1403/1404).
func statusCodeToRetCode(status int) int {
	switch status {
	case http.StatusBadRequest:
		return 1400
	case http.StatusUnauthorized:
		return 1401
	case http.StatusForbidden:
		return 1403
	case http.StatusNotFound:
		return 1404
	}
	return 1400
}

// transportName renders a listener type for logs.
func transportName(t string) string {
	switch t {
	case store.ListenerHTTPServer:
		return "HTTP"
	case store.ListenerHTTPSSE:
		return "SSE"
	case store.ListenerWSServer:
		return "WS"
	case store.ListenerWSClient:
		return "反向WS"
	case store.ListenerHTTPClient:
		return "HTTP上报"
	}
	return t
}

// ---- HTTP server & SSE ----

func (rt *listenerRuntime) handleHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	w.Header().Set("Content-Type", "application/json")
	if !rt.authorized(r) {
		rt.unauthorized(w, r)
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
	// Spec: an unsupported POST Content-Type is 406. An empty Content-Type is
	// tolerated because plenty of clients omit it on parameter-less actions.
	if ct := r.Header.Get("Content-Type"); ct != "" && !isSupportedContentType(ct) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotAcceptable)
		_ = json.NewEncoder(w).Encode(onebot.Failed(nil, 1400, "不支持的 Content-Type: "+ct))
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(onebot.Failed(nil, 400, "读取请求体失败"))
		return
	}
	// Shells on Windows often send GBK bytes, which decode to mojibake. Say so
	// explicitly instead of letting the user blame the adapter.
	if !utf8.Valid(raw) {
		rt.app.Log("warn", fmt.Sprintf(
			"[%s/#%d HTTP] 请求体不是合法的 UTF-8（很可能是 curl/PowerShell 的终端编码问题，而非适配器）：%s",
			rt.cfg.Name, rt.cfg.ID, ellipsis(string(raw))))
	}
	var act onebot.Action
	// An empty body is valid for the "/{action}" style, where all parameters may
	// live in the query string.
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &act); err != nil {
			// OneBot also allows urlencoded form parameters on the action URL.
			if ferr := r.ParseForm(); ferr == nil && len(r.PostForm) > 0 {
				act = actionFromForm(r, nil)
			}
		}
	}

	// OneBot 11 defines two URL styles and both must work:
	//   POST /                 with {"action": "...", "params": {...}}
	//   POST /{action}         with params at top level (as ?query or body)
	// Clients of the second style were previously answered with data:null
	// because the path segment was never turned into an action name.
	if pathAction := actionFromPath(r.URL.Path, rt.cfg.Path); pathAction != "" {
		if act.Action == "" {
			act.Action = pathAction
		}
		// Merge top-level params and query args into act.Params.
		merged := map[string]any{}
		for k, v := range act.Params {
			merged[k] = v
		}
		if q := r.URL.Query(); len(q) > 0 {
			for k, vals := range q {
				if len(vals) > 0 && isParamName(k) {
					merged[k] = vals[0]
				}
			}
		}
		if extra := topLevelParams(raw); extra != nil {
			for k, v := range extra {
				if _, exists := merged[k]; !exists {
					merged[k] = v
				}
			}
		}
		act.Params = merged
	}

	if act.Action == "" {
		// Some clients POST events to the API endpoint by mistake; accept silently.
		_ = json.NewEncoder(w).Encode(onebot.OK(nil, nil))
		return
	}
	rt.traceReceive("HTTP", act.Action, act, act.Echo)
	start := time.Now()
	resp, err := rt.app.HandleAction(rt.bot.SelfNum(), act)
	if err != nil {
		rt.writeActionError(w, act, err)
		return
	}
	rt.traceReply("HTTP", act.Action, resp)
	rt.app.Log("debug", "API "+act.Action+": "+map[bool]string{true: "ok", false: "failed"}[resp.Status == "ok"]+
		" 耗时="+time.Since(start).String())
	_ = json.NewEncoder(w).Encode(resp)
}

// writeActionError renders a failed action per the OneBot 11 spec: an unknown
// API is HTTP 404 (the status code is the signal), while a known-but-unsupported
// API is an ordinary business failure (HTTP 200 with a non-zero retcode).
func (rt *listenerRuntime) writeActionError(w http.ResponseWriter, act onebot.Action, err error) {
	var notFound *onebot.ErrActionNotFound
	if errors.As(err, &notFound) {
		rt.app.Log("debug", "API "+act.Action+": 未实现，按标准返回 404")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		// Spec: no body is required, but a short JSON message helps debugging.
		_ = json.NewEncoder(w).Encode(onebot.Failed(act.Echo, 1404, notFound.Error()))
		return
	}
	var unsupported *onebot.ErrUnsupported
	retCode := 1400
	if errors.As(err, &unsupported) {
		retCode = 1403
	}
	resp := onebot.Failed(act.Echo, retCode, err.Error())
	rt.traceReply("HTTP", act.Action, resp)
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
		rt.unauthorized(w, r)
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
	// Trace every outbound event once, regardless of transport.
	if rt.traceEnabled() {
		var ev onebot.Event
		transport := map[string]string{
			store.ListenerHTTPSSE:    "SSE",
			store.ListenerHTTPServer: "HTTP",
			store.ListenerWSServer:   "WS",
			store.ListenerWSClient:   "反向WS",
			store.ListenerHTTPClient: "HTTP上报",
		}[rt.cfg.Type]
		if err := json.Unmarshal(data, &ev); err == nil {
			rt.traceEvent(transport, ev)
		} else {
			rt.traceRaw(transport, "→ 原始事件", string(data))
		}
	}

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
	if rt.traceEnabled() {
		rt.app.Log("debug", fmt.Sprintf("[%s/#%d WS] → 发送帧 %s", rt.cfg.Name, rt.cfg.ID, ellipsis(string(data))))
	}
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		_ = c.Close()
	}
}

func (rt *listenerRuntime) handleWSServer(w http.ResponseWriter, r *http.Request, up *websocket.Upgrader) {
	if !rt.authorized(r) {
		rt.unauthorized(w, r)
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
			if rt.traceEnabled() {
				rt.traceRaw("WS", "✕ 连接关闭", err.Error())
			}
			return
		}
		if rt.traceEnabled() {
			rt.traceRaw("WS", "← 收到帧", string(raw))
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
		rt.traceReceive("WS", act.Action, act, act.Echo)
		start := time.Now()
		resp, aerr := rt.app.HandleAction(rt.bot.SelfNum(), act)
		if aerr != nil {
			resp = onebot.Failed(act.Echo, wsRetCode(aerr), aerr.Error())
		}
		rt.traceReply("WS", act.Action, resp)
		rt.app.Log("debug", fmt.Sprintf("WS 动作 %s 耗时=%s", act.Action, time.Since(start).String()))
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
				if rt.traceEnabled() {
					rt.traceRaw("HTTP上报", "→ POST "+target, string(data))
				}
				resp, err := hc.Do(req)
				if err != nil {
					rt.app.Log("warn", fmt.Sprintf("上报事件到 %s 失败: %v", target, err))
					return
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				resp.Body.Close()
				if rt.traceEnabled() {
					rt.traceRaw("HTTP上报", fmt.Sprintf("← HTTP %d", resp.StatusCode), string(body))
				}
				if resp.StatusCode >= 400 {
					rt.app.Log("warn", fmt.Sprintf("上报事件到 %s 返回 %d: %s", target, resp.StatusCode, string(body)))
				}
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
			if rt.traceEnabled() {
				rt.traceRaw("反向WS", "✕ 连接关闭", err.Error())
			}
			return err
		}
		if rt.traceEnabled() {
			rt.traceRaw("反向WS", "← 收到帧", string(raw))
		}
		var act onebot.Action
		if err := json.Unmarshal(raw, &act); err != nil {
			resp, _ := json.Marshal(onebot.Failed(nil, 400, "无效的 JSON"))
			rt.writeWS(conn, resp)
			continue
		}
		rt.traceReceive("反向WS", act.Action, act, act.Echo)
		start := time.Now()
		resp, aerr := rt.app.HandleAction(rt.bot.SelfNum(), act)
		if aerr != nil {
			resp = onebot.Failed(act.Echo, wsRetCode(aerr), aerr.Error())
		}
		rt.traceReply("反向WS", act.Action, resp)
		rt.app.Log("debug", fmt.Sprintf("反向WS 动作 %s 耗时=%s", act.Action, time.Since(start).String()))
		out, _ := json.Marshal(resp)
		rt.writeWS(conn, out)
	}
}

var _ = qq.APIError{}

// actionFromPath extracts the action name from a OneBot "POST /{action}" URL,
// honouring the listener's configured path prefix. Returns "" for the bare
// prefix, which means the legacy "POST /" body-carried action style.
func actionFromPath(urlPath, basePath string) string {
	p := strings.Trim(urlPath, "/")
	if base := strings.Trim(basePath, "/"); base != "" {
		if p == base {
			return ""
		}
		if rest, ok := strings.CutPrefix(p, base+"/"); ok {
			p = rest
		} else {
			return ""
		}
	}
	if p == "" || strings.Contains(p, "/") {
		return ""
	}
	// Reserved non-action endpoints.
	switch p {
	case "_events", "events", "api", "event", "universal":
		return ""
	}
	return p
}

// isParamName filters out the meaning-bearing query keys so they are not
// forwarded as action parameters.
func isParamName(k string) bool {
	switch k {
	case "access_token", "token", "echo":
		return false
	}
	return k != ""
}

// topLevelParams reads OneBot-style flat parameters ({"group_id":1,"message":...})
// from a request body that has no "params" object.
func topLevelParams(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range m {
		switch k {
		case "action", "params", "echo", "access_token", "token":
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// actionFromForm builds an action from urlencoded form fields.
func actionFromForm(r *http.Request, pre map[string]any) onebot.Action {
	out := onebot.Action{Params: map[string]any{}}
	for k, v := range pre {
		out.Params[k] = v
	}
	for k, vals := range r.PostForm {
		if len(vals) == 0 {
			continue
		}
		switch k {
		case "action":
			out.Action = vals[0]
		case "echo":
			out.Echo = vals[0]
		case "access_token", "token":
		default:
			out.Params[k] = vals[0]
		}
	}
	return out
}

// wsRetCode maps a dispatch error onto the WebSocket retcode scale defined by
// the spec (HTTP status codes shifted into the 1xxx range).
func wsRetCode(err error) int {
	var notFound *onebot.ErrActionNotFound
	if errors.As(err, &notFound) {
		return 1404
	}
	var unsupported *onebot.ErrUnsupported
	if errors.As(err, &unsupported) {
		return 1403
	}
	return 1400
}

// isSupportedContentType accepts the two encodings the spec allows for API
// calls: JSON and urlencoded forms.
func isSupportedContentType(ct string) bool {
	base := strings.TrimSpace(strings.Split(ct, ";")[0])
	switch strings.ToLower(base) {
	case "application/json", "application/x-www-form-urlencoded":
		return true
	}
	return false
}
