package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Event is a dispatched gateway event.
type Event struct {
	ID string          `json:"id"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
	S  int64           `json:"s"`
}

type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
	ID string          `json:"id,omitempty"`
}

// GatewayStatus is reported to observers on each connection state change.
type Status struct {
	State       string `json:"state"` // offline | connecting | online | resuming | error
	Message     string `json:"message,omitempty"`
	SelfID      string `json:"self_id,omitempty"`
	Username    string `json:"username,omitempty"`
	ConnectedAt int64  `json:"connected_at,omitempty"`
	LastEventAt int64  `json:"last_event_at,omitempty"`
	EventCount  int64  `json:"event_count"`
	ErrorCount  int64  `json:"error_count"`
	LastError   string `json:"last_error,omitempty"`
}

// Gateway keeps a single bot's WebSocket connection alive.
type Gateway struct {
	Client  *Client
	Intents int64

	OnEvent  func(*Event)
	OnStatus func(Status)

	mu      sync.Mutex
	conn    *websocket.Conn
	stop    chan struct{}
	done    chan struct{}
	running bool
	status  Status
	lastErr string
	events  atomic.Int64
}

func (g *Gateway) statusSnapshot() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.status
	st.EventCount = g.events.Load()
	st.LastError = g.lastErr
	return st
}

// Status returns the current connection state.
func (g *Gateway) Status() Status { return g.statusSnapshot() }

func (g *Gateway) setStatus(st Status) {
	g.mu.Lock()
	// Merge rather than replace: partial updates (heartbeats, RESUMED) must not
	// drop the identity learned from READY.
	if st.SelfID == "" {
		st.SelfID = g.status.SelfID
	}
	if st.Username == "" {
		st.Username = g.status.Username
	}
	if st.ConnectedAt == 0 {
		st.ConnectedAt = g.status.ConnectedAt
	}
	if st.LastEventAt == 0 {
		st.LastEventAt = g.status.LastEventAt
	}
	if st.State == "" {
		st.State = g.status.State
	}
	// A successful online/resume clears the last error.
	if st.State == "online" && st.Message == "" {
		g.lastErr = ""
	}
	g.status = st
	g.mu.Unlock()
	if g.OnStatus != nil {
		g.OnStatus(g.statusSnapshot())
	}
}

func (g *Gateway) reportError(err error) {
	g.mu.Lock()
	g.lastErr = err.Error()
	g.status.ErrorCount++
	g.mu.Unlock()
	g.setStatus(Status{State: "error", Message: err.Error()})
}

// Start launches the connection loop; it returns immediately.
func (g *Gateway) Start() {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.stop = make(chan struct{})
	g.done = make(chan struct{})
	g.mu.Unlock()
	go g.loop()
}

// Stop closes the gateway and waits for the loop to unwind.
func (g *Gateway) Stop() {
	g.mu.Lock()
	if !g.running {
		g.mu.Unlock()
		return
	}
	g.running = false
	close(g.stop)
	if g.conn != nil {
		g.conn.Close()
	}
	done := g.done
	g.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (g *Gateway) runStopped() bool {
	select {
	case <-g.stop:
		return true
	default:
		return false
	}
}

func (g *Gateway) loop() {
	defer close(g.done)
	defer func() {
		g.mu.Lock()
		g.conn = nil
		g.mu.Unlock()
	}()
	g.setStatus(Status{State: "connecting"})

	var (
		sessionID string
		seq       int64
		hasSeq    bool
		attempt   int
	)
	for {
		if g.runStopped() {
			g.setStatus(Status{State: "offline"})
			return
		}
		resume := sessionID != "" && attempt < 5
		if resume {
			g.setStatus(Status{State: "resuming"})
		}
		gotReady, newSession, newSeq, gotSeq := g.connectOnce(resume, sessionID, seq, hasSeq)
		if newSession != "" {
			sessionID = newSession
		}
		if gotSeq {
			seq, hasSeq = newSeq, true
		}
		if gotReady && gotSeq {
			attempt = 0
		}
		if g.runStopped() {
			g.setStatus(Status{State: "offline"})
			return
		}
		attempt++
		backoff := time.Duration(attempt) * 2 * time.Second
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
		if resume && gotReady {
			// Connection was healthy before dropping; retry quickly once.
			backoff = time.Second
		}
		select {
		case <-g.stop:
			g.setStatus(Status{State: "offline"})
			return
		case <-time.After(backoff):
		}
	}
}

// connectOnce opens one connection and serves it until it drops or Stop is called.
func (g *Gateway) connectOnce(resume bool, sessionID string, seq int64, hasSeq bool) (ready bool, newSession string, lastSeq int64, gotSeq bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	gwURL, err := g.Client.GatewayURL(ctx)
	cancel()
	if err != nil {
		g.reportError(fmt.Errorf("获取网关地址失败: %w", err))
		return
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 20 * time.Second
	conn, _, err := dialer.Dial(gwURL, nil)
	if err != nil {
		g.reportError(fmt.Errorf("连接 QQ 网关失败: %w", err))
		return
	}
	g.mu.Lock()
	g.conn = conn
	g.mu.Unlock()
	defer conn.Close()

	// Heartbeat state is shared between the writer goroutine and the reader loop.
	var (
		hbMu       sync.Mutex
		interval   = 30 * time.Second
		lastACK    = time.Now()
		lastSeqRef = func() int64 { return 0 }
	)
	write := func(v any) error {
		hbMu.Lock()
		defer hbMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return conn.WriteJSON(v)
	}

	// The reader loop owns seq; the heartbeat needs it, so publish a copy.
	setSeq := func(n int64) {
		hbMu.Lock()
		lastSeqRef = func() int64 { return n }
		hbMu.Unlock()
	}

	stopHB := make(chan struct{})
	defer close(stopHB)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		lastSend := time.Now()
		for {
			select {
			case <-stopHB:
				return
			case <-t.C:
				hbMu.Lock()
				iv := interval
				ackedAt := lastACK
				hbMu.Unlock()

				// QQ drops dead connections by expecting heartbeats; missing
				// several ACKs means the socket is a zombie, so force a redial.
				if time.Since(ackedAt) > iv*2+30*time.Second {
					slog.Warn("heartbeat ACK timeout, reconnecting", "app_id", g.Client.AppID)
					conn.Close()
					return
				}
				if time.Since(lastSend) < iv {
					continue
				}
				hbMu.Lock()
				d := lastSeqRef()
				hbMu.Unlock()
				p := map[string]any{"op": 1, "d": nil}
				if d > 0 {
					p["d"] = d
				}
				if err := write(p); err != nil {
					conn.Close()
					return
				}
				lastSend = time.Now()
			}
		}
	}()

	readySeen := false
	for {
		var p payload
		if err := conn.ReadJSON(&p); err != nil {
			g.reportError(fmt.Errorf("网关连接中断: %w", err))
			return readySeen, sessionID, seq, hasSeq
		}
		switch p.Op {
		case 10: // Hello
			var d struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			_ = json.Unmarshal(p.D, &d)
			if d.HeartbeatInterval > 0 {
				hbMu.Lock()
				interval = time.Duration(d.HeartbeatInterval)*time.Millisecond - time.Second
				hbMu.Unlock()
			}
			tok, err := g.Client.Token(context.Background())
			if err != nil {
				g.reportError(fmt.Errorf("获取 access_token 失败: %w", err))
				return readySeen, sessionID, seq, hasSeq
			}
			if resume {
				if err := write(map[string]any{"op": 6, "d": map[string]any{
					"token": "QQBot " + tok, "session_id": sessionID, "seq": seq,
				}}); err != nil {
					return readySeen, sessionID, seq, hasSeq
				}
			} else {
				if err := write(map[string]any{"op": 2, "d": map[string]any{
					"token":      "QQBot " + tok,
					"intents":    g.Intents,
					"shard":      []int{0, 1},
					"properties": map[string]string{"$os": "linux", "$browser": "qqbot-onebot", "$device": "qqbot-onebot"},
				}}); err != nil {
					return readySeen, sessionID, seq, hasSeq
				}
			}
		case 11: // Heartbeat ACK
			hbMu.Lock()
			lastACK = time.Now()
			hbMu.Unlock()
		case 0: // Dispatch
			if p.S != nil {
				seq, hasSeq = *p.S, true
				setSeq(seq)
			}
			switch p.T {
			case "READY":
				var d struct {
					SessionID string `json:"session_id"`
					User      struct {
						ID       string `json:"id"`
						Username string `json:"username"`
					} `json:"user"`
				}
				_ = json.Unmarshal(p.D, &d)
				sessionID = d.SessionID
				readySeen = true
				g.mu.Lock()
				g.lastErr = ""
				g.mu.Unlock()
				g.setStatus(Status{
					State:       "online",
					SelfID:      d.User.ID,
					Username:    d.User.Username,
					ConnectedAt: time.Now().Unix(),
					LastEventAt: time.Now().Unix(),
				})
				slog.Info("qq gateway ready", "app_id", g.Client.AppID, "bot", d.User.Username, "self_id", d.User.ID)
				continue
			case "RESUMED":
				readySeen = true
				g.setStatus(Status{State: "online", LastEventAt: time.Now().Unix()})
				slog.Info("qq gateway resumed", "app_id", g.Client.AppID)
				continue
			}
			g.events.Add(1)
			// Keep last-event timestamp fresh without spamming observers.
			g.mu.Lock()
			g.status.LastEventAt = time.Now().Unix()
			g.mu.Unlock()
			if g.OnEvent != nil && len(p.D) > 0 {
				g.OnEvent(&Event{ID: p.ID, T: p.T, D: p.D, S: seq})
			}
		case 7: // Reconnect requested
			slog.Info("qq gateway asked to reconnect", "app_id", g.Client.AppID)
			conn.Close()
			return readySeen, sessionID, seq, hasSeq
		case 9: // Invalid session
			slog.Warn("qq gateway invalid session", "app_id", g.Client.AppID)
			sessionID = ""
			g.reportError(fmt.Errorf("网关会话无效（Invalid Session），将重新鉴权"))
			conn.Close()
			return false, "", 0, false
		}
	}
}
