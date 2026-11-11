package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var consoleUpgrader = websocket.Upgrader{
	CheckOrigin:  func(*http.Request) bool { return true },
	Subprotocols: []string{"onebot.v11"},
}

// consoleWS streams live console events (bot status, logs, OneBot events).
func (s *Server) consoleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	subID, ch := s.app.SubscribeConsole()
	defer s.app.UnsubscribeConsole(subID)

	// Prime with the current state so a fresh page has context immediately.
	prime := map[string]any{
		"type":        "hello",
		"server_time": time.Now().Unix(),
		"logs":        s.app.Logs(),
		"recent":      s.app.RecentEvents(),
	}
	if b, err := json.Marshal(prime); err == nil {
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			return
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-done:
			return
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case data, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}
