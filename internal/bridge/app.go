// Package bridge wires the QQ official bot platform to OneBot 11 clients.
package bridge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

// buildVersion is reported by get_version_info; overridable in tests.
var buildVersion = "dev"

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func displayLabel(rec store.Bot) string {
	if strings.TrimSpace(rec.Name) != "" {
		return rec.Name
	}
	return "机器人 " + rec.AppID
}

// RecentIncoming remembers the last inbound message of a chat so replies can go
// out as passive replies (QQ only allows active messages under strict quotas).
type RecentIncoming struct {
	EventID  string
	Seq      int64
	At       time.Time
	Sender   string
	GroupNum int64
}

// SentMessage links a synthetic OneBot message id to the QQ message and chat.
type SentMessage struct {
	Kind   string // group | private
	OpenID string
	QQID   string
	At     time.Time
}

type Bot struct {
	rec    store.Bot
	client *qq.Client
	gw     *qq.Gateway

	mu           sync.Mutex
	status       qq.Status
	listeners    []*listenerRuntime
	recent       map[string]*RecentIncoming
	sent         map[int64]*SentMessage
	msgCounter   int64
	selfNum      int64
	selfUsername string
}

// App owns every bot runtime and fans events out to console + listeners.
type App struct {
	st *store.Store

	mu   sync.Mutex
	bots map[int64]*Bot

	// startLocks serialises Start/Stop per bot so rapid console clicks cannot
	// spawn two gateways for one bot.
	startMu    sync.Mutex
	startLocks map[int64]*sync.Mutex

	startedAt time.Time

	logs       *ringLog
	consoleMu  sync.Mutex
	consoleSub map[int]chan []byte
	subID      atomic.Int64

	recentMu sync.Mutex
	recentEv []ConsoleEvent

	counters struct {
		events  atomic.Int64
		actions atomic.Int64
	}
}

func NewApp(st *store.Store) *App {
	return &App{
		st:         st,
		bots:       map[int64]*Bot{},
		startLocks: map[int64]*sync.Mutex{},
		startedAt:  time.Now(),
		logs:       newRingLog(500),
		consoleSub: map[int]chan []byte{},
	}
}

func (a *App) lockFor(id int64) *sync.Mutex {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	l, ok := a.startLocks[id]
	if !ok {
		l = &sync.Mutex{}
		a.startLocks[id] = l
	}
	return l
}

// SetBuildVersion records the compiled version for version reporting.
func (a *App) SetBuildVersion(v string) {
	if strings.TrimSpace(v) != "" {
		buildVersion = v
	}
}

func (a *App) Store() *store.Store { return a.st }

// StartAll brings up every enabled bot from the database.
func (a *App) StartAll() {
	bots, err := a.st.ListBots()
	if err != nil {
		slog.Error("load bots", "err", err)
		return
	}
	for i := range bots {
		if bots[i].Enabled {
			if err := a.StartBot(bots[i].ID); err != nil {
				slog.Error("start bot", "id", bots[i].ID, "err", err)
			}
		}
	}
}

func (a *App) StopAll() {
	a.mu.Lock()
	ids := make([]int64, 0, len(a.bots))
	for id := range a.bots {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.StopBot(id)
	}
}

// StartBot (re)starts a bot runtime from the database state.
func (a *App) StartBot(id int64) error {
	lk := a.lockFor(id)
	lk.Lock()
	defer lk.Unlock()

	rec, err := a.st.GetBot(id)
	if err != nil {
		return err
	}
	if rec.AppID == "" || rec.Secret == "" {
		return fmt.Errorf("AppID 或 AppSecret 未填写")
	}
	a.stopBotRuntime(id)

	b := &Bot{
		rec:    rec,
		client: qq.NewClient(rec.AppID, rec.Secret, rec.Sandbox),
		recent: map[string]*RecentIncoming{},
		sent:   map[int64]*SentMessage{},
		status: qq.Status{State: "connecting"},
	}
	b.msgCounter = time.Now().UnixNano() / 1000
	a.mu.Lock()
	a.bots[id] = b
	a.mu.Unlock()

	intents := rec.Intents
	if intents == 0 {
		intents = 1 << 25
	}
	b.gw = &qq.Gateway{Client: b.client, Intents: intents}
	b.gw.OnStatus = func(st qq.Status) {
		b.mu.Lock()
		prev := b.status
		b.status = st
		if st.SelfID != "" {
			b.selfUsername = st.Username
			if n, ok := selfIDToInt64(st.SelfID); ok {
				b.selfNum = n
			}
			// Persist identity so the console shows it before a reconnect.
			if err := a.st.SetBotIdentity(id, st.SelfID, st.Username); err == nil {
				rec.SelfID, rec.Username = st.SelfID, st.Username
			}
		}
		b.mu.Unlock()
		a.publishJSON(map[string]any{"type": "status", "bot_id": id, "status": st})
		// Surface transitions in the log so failures are visible without
		// opening the bot's detail view.
		switch {
		case st.State == "error" && prev.State != "error":
			a.Log("error", fmt.Sprintf("%s 网关错误: %s", displayLabel(rec), st.Message))
		case st.State == "online" && prev.State != "online":
			a.Log("info", fmt.Sprintf("%s 已连接 QQ 网关（%s）", displayLabel(rec), st.Username))
		case st.State == "offline" && prev.State != "offline":
			a.Log("info", fmt.Sprintf("%s 网关已离线", displayLabel(rec)))
		}
	}
	b.gw.OnEvent = func(ev *qq.Event) { a.handleQQEvent(b, ev) }
	b.gw.Start()

	// Bring up its configured listeners.
	all, err := a.st.ListListeners()
	if err != nil {
		return err
	}
	for _, l := range all {
		if l.BotID == id && l.Enabled {
			if rt, err := a.startListener(b, l); err != nil {
				slog.Error("start listener", "listener", l.Name, "err", err)
				a.publishJSON(map[string]any{"type": "log", "level": "error", "msg": fmt.Sprintf("监听 %s 启动失败: %v", l.Name, err)})
			} else {
				b.mu.Lock()
				b.listeners = append(b.listeners, rt)
				b.mu.Unlock()
			}
		}
	}
	a.publishJSON(map[string]any{"type": "status", "bot_id": id, "status": b.gw.Status()})
	return nil
}

func (a *App) StopBot(id int64) {
	a.stopBotRuntime(id)
}

// stopBotRuntime detaches a bot's runtime and tears it down outside the app lock,
// so a concurrent StartBot only blocks on the per-bot start lock.
func (a *App) stopBotRuntime(id int64) {
	a.mu.Lock()
	b := a.bots[id]
	delete(a.bots, id)
	a.mu.Unlock()
	if b == nil {
		return
	}
	b.mu.Lock()
	rts := b.listeners
	b.listeners = nil
	b.mu.Unlock()
	for _, rt := range rts {
		rt.stop()
	}
	b.gw.Stop()
}

// RestartBot reloads config from the database.
func (a *App) RestartBot(id int64) error {
	a.StopBot(id)
	rec, err := a.st.GetBot(id)
	if err != nil {
		return err
	}
	if !rec.Enabled {
		return nil
	}
	return a.StartBot(id)
}

// RestartListeners reloads only a bot's OneBot transports, leaving the QQ
// gateway connection untouched so in-flight events are not dropped.
func (a *App) RestartListeners(id int64) error {
	a.mu.Lock()
	b := a.bots[id]
	a.mu.Unlock()
	if b == nil {
		return a.RestartBot(id)
	}
	b.mu.Lock()
	old := b.listeners
	b.listeners = nil
	b.mu.Unlock()
	for _, rt := range old {
		rt.stop()
	}
	if !b.rec.Enabled {
		return nil
	}
	all, err := a.st.ListListeners()
	if err != nil {
		return err
	}
	for _, l := range all {
		if l.BotID != id || !l.Enabled {
			continue
		}
		rt, err := a.startListener(b, l)
		if err != nil {
			a.Log("error", fmt.Sprintf("监听 %s 启动失败: %v", l.Name, err))
			continue
		}
		b.mu.Lock()
		b.listeners = append(b.listeners, rt)
		b.mu.Unlock()
	}
	return nil
}

func (a *App) Bot(id int64) *Bot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bots[id]
}

func (a *App) Bots() []*Bot {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*Bot, 0, len(a.bots))
	for _, b := range a.bots {
		out = append(out, b)
	}
	return out
}

// SelfNum returns the numeric bot id exposed to OneBot clients.
func (b *Bot) SelfNum() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selfNum != 0 {
		return b.selfNum
	}
	if n, ok := selfIDToInt64(b.rec.SelfID); ok {
		return n
	}
	// Stable fallback so clients that validate self_id still work.
	return -(b.rec.ID + 100000)
}

// selfIDToInt64 maps a QQ account id onto an int64. Official bot ids are 20
// digits and overflow int64 (max 9223372036854775807), so values that do not
// fit are folded into range. The mapping is deterministic, so self_id stays
// the same across restarts, which is what OneBot clients rely on.
func selfIDToInt64(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := parseInt64(s); err == nil && n > 0 {
		return n, true
	}
	// Fold the decimal digits into the positive int64 range.
	var acc uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			// Not a numeric id at all; let the caller fall back.
			return 0, false
		}
		acc = acc*10 + uint64(r-'0')
	}
	if acc == 0 {
		return 0, false
	}
	const maxPositive = uint64(1) << 62
	return int64(acc % maxPositive), true
}

func (b *Bot) Status() qq.Status {
	b.mu.Lock()
	st := b.status
	selfID, name := b.rec.SelfID, b.rec.Username
	b.mu.Unlock()
	if st.SelfID == "" {
		st.SelfID = selfID
		st.Username = name
	}
	return st
}

// Broadcast delivers a OneBot event to every listener attached to the bot that
// produced it, then mirrors it to the console.
func (a *App) Broadcast(ev onebot.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Error("marshal event", "err", err)
		return
	}
	a.counters.events.Add(1)
	a.noteRecent(ev)

	a.mu.Lock()
	matched := make([]*listenerRuntime, 0, 4)
	for _, bot := range a.bots {
		if bot.SelfNum() != ev.SelfID {
			continue
		}
		bot.mu.Lock()
		matched = append(matched, bot.listeners...)
		bot.mu.Unlock()
	}
	a.mu.Unlock()

	for _, rt := range matched {
		rt.sendEvent(data)
	}
	a.publishJSON(map[string]any{"type": "event", "self_id": ev.SelfID, "post_type": ev.PostType, "event": json.RawMessage(data)})
}

func (a *App) noteRecent(ev onebot.Event) {
	summary := ""
	if ev.RawMessage != "" {
		summary = ev.RawMessage
	}
	if len([]rune(summary)) > 120 {
		summary = string([]rune(summary)[:120]) + "…"
	}
	a.recentMu.Lock()
	a.recentEv = append(a.recentEv, ConsoleEvent{
		Time: ev.Time, PostType: ev.PostType, MessageType: ev.MessageType,
		GroupID: ev.GroupID, UserID: ev.UserID, Summary: summary, SelfID: ev.SelfID,
	})
	if len(a.recentEv) > 200 {
		a.recentEv = a.recentEv[len(a.recentEv)-200:]
	}
	a.recentMu.Unlock()
}

type ConsoleEvent struct {
	Time        int64  `json:"time"`
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type,omitempty"`
	GroupID     int64  `json:"group_id,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
	SelfID      int64  `json:"self_id"`
	Summary     string `json:"summary"`
}

func (a *App) RecentEvents() []ConsoleEvent {
	a.recentMu.Lock()
	defer a.recentMu.Unlock()
	out := make([]ConsoleEvent, len(a.recentEv))
	copy(out, a.recentEv)
	return out
}

// InjectEvent feeds a synthetic QQ gateway event into a running bot. It exists
// for the integration harness and is only reachable when QQBOT_TEST_INJECT=1.
func (a *App) InjectEvent(botID int64, eventType string, data map[string]any) error {
	b := a.Bot(botID)
	if b == nil {
		return fmt.Errorf("机器人未运行")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	a.handleQQEvent(b, &qq.Event{ID: "inject", T: eventType, D: raw})
	return nil
}

func (a *App) SubscribeConsole() (int, chan []byte) {
	a.subID.Add(1)
	id := int(a.subID.Load())
	ch := make(chan []byte, 128)
	a.consoleMu.Lock()
	a.consoleSub[id] = ch
	a.consoleMu.Unlock()
	// Prime the client with server info.
	return id, ch
}

func (a *App) UnsubscribeConsole(id int) {
	a.consoleMu.Lock()
	if ch, ok := a.consoleSub[id]; ok {
		delete(a.consoleSub, id)
		close(ch)
	}
	a.consoleMu.Unlock()
}

func (a *App) publishJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	// Any line of the ring log is also broadcast for live tailing.
	if m, ok := v.(map[string]any); ok {
		if lv, isLog := m["level"]; isLog {
			a.logs.add(LogEntry{Time: time.Now().UnixMilli(), Level: lv.(string), Msg: m["msg"].(string)})
		}
	}
	a.consoleMu.Lock()
	defer a.consoleMu.Unlock()
	for id, ch := range a.consoleSub {
		select {
		case ch <- data:
		default:
			// Slow console client: drop rather than stall the bridge.
		}
		_ = id
	}
}

func (a *App) Logs() []LogEntry { return a.logs.snapshot() }

func (a *App) Uptime() int64 { return int64(time.Since(a.startedAt).Seconds()) }

func (a *App) StartedAtUnix() int64 { return a.startedAt.Unix() }

func (a *App) Counters() (events, actions int64) {
	return a.counters.events.Load(), a.counters.actions.Load()
}

// LogLevel records a console log line.
func (a *App) Log(level, msg string) {
	a.publishJSON(map[string]any{"type": "log", "level": level, "msg": msg})
	slog.Info(msg, "level", level)
}
