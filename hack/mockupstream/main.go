// Command mockupstream runs a fake QQ open-platform server for integration tests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	addr = flag.String("addr", "127.0.0.1:19090", "listen address")
	mu   sync.Mutex
	sent []string

	wsMu   sync.Mutex
	wsConn *websocket.Conn
)

func main() {
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "mock-token", "expires_in": "7200"})
	})
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"url": "ws://" + *addr + "/websocket"})
	})
	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "8888888", "username": "MockBot"})
	})
	mux.HandleFunc("/debug/sent", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(sent)
	})
	// Event injection for tests: POST {"t": "...", "d": {...}}
	mux.HandleFunc("/debug/inject", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			T string          `json:"t"`
			D json.RawMessage `json:"d"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		wsMu.Lock()
		c := wsConn
		wsMu.Unlock()
		if c == nil {
			http.Error(w, "no connection", 503)
			return
		}
		_ = c.WriteJSON(map[string]any{"id": "EVT-x", "op": 0, "s": time.Now().UnixNano(), "t": body.T, "d": body.D})
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": "MSG" + time.Now().Format("150405")})
	})
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wsMu.Lock()
		wsConn = c
		wsMu.Unlock()
		_ = c.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 45000}})
		for {
			var p map[string]any
			if err := c.ReadJSON(&p); err != nil {
				return
			}
			switch p["op"].(float64) {
			case 2:
				_ = c.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{
					"version": 1, "session_id": "mock-session",
					"user":  map[string]any{"id": "8888888", "username": "MockBot", "bot": true},
					"shard": []int{0, 1},
				}})
			case 6:
				_ = c.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "RESUMED", "d": ""})
			case 1:
				_ = c.WriteJSON(map[string]any{"op": 11})
			}
		}
	})

	fmt.Println("mock upstream on", *addr)
	_ = (&http.Server{Addr: *addr, Handler: mux}).ListenAndServe()
}
