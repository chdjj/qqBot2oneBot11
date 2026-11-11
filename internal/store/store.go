// Package store provides a small SQLite-backed persistence layer.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Bot struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	AppID     string `json:"app_id"`
	Secret    string `json:"secret"`
	Sandbox   bool   `json:"sandbox"`
	Intents   int64  `json:"intents"`
	Enabled   bool   `json:"enabled"`
	SelfID    string `json:"self_id"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"created_at"`
}

// Listener types mirror the transports supported by the bridge.
const (
	ListenerHTTPServer = "http_server" // OneBot HTTP API + event POST to post_url
	ListenerHTTPSSE    = "http_sse"    // OneBot HTTP API + SSE event stream
	ListenerHTTPClient = "http_client" // events POSTed to url
	ListenerWSServer   = "ws_server"   // forward WebSocket (OneBot acts as server)
	ListenerWSClient   = "ws_client"   // reverse WebSocket (OneBot dials url)
)

type Listener struct {
	ID        int64  `json:"id"`
	BotID     int64  `json:"bot_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	PostURL   string `json:"post_url"`
	Token     string `json:"token"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

type Group struct {
	ID        int64  `json:"id"`
	BotID     int64  `json:"bot_id"`
	OpenID    string `json:"open_id"`
	NumID     int64  `json:"num_id"`
	Name      string `json:"name"`
	LastSeen  int64  `json:"last_seen"`
	CreatedAt int64  `json:"created_at"`
}

var ErrNotFound = errors.New("not found")

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS bots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			app_id TEXT NOT NULL UNIQUE,
			secret TEXT NOT NULL DEFAULT '',
			sandbox INTEGER NOT NULL DEFAULT 0,
			intents INTEGER NOT NULL DEFAULT 33554432,
			enabled INTEGER NOT NULL DEFAULT 0,
			self_id TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS listeners (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			host TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			path TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			post_url TEXT NOT NULL DEFAULT '',
			token TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL,
			open_id TEXT NOT NULL,
			num_id INTEGER NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			last_seen INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(bot_id, open_id),
			UNIQUE(bot_id, num_id)
		)`,
		`CREATE TABLE IF NOT EXISTS openid_map (
			bot_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			open_id TEXT NOT NULL,
			num_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(bot_id, kind, open_id),
			UNIQUE(bot_id, kind, num_id)
		)`,
		`CREATE TABLE IF NOT EXISTS id_seq (
			bot_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			next INTEGER NOT NULL,
			PRIMARY KEY(bot_id, kind)
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

// ---- bots ----

func (s *Store) ListBots() ([]Bot, error) {
	rows, err := s.db.Query(`SELECT id, name, app_id, secret, sandbox, intents, enabled, self_id, username, created_at FROM bots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bot
	for rows.Next() {
		var b Bot
		if err := rows.Scan(&b.ID, &b.Name, &b.AppID, &b.Secret, &b.Sandbox, &b.Intents, &b.Enabled, &b.SelfID, &b.Username, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBot(id int64) (Bot, error) {
	var b Bot
	err := s.db.QueryRow(`SELECT id, name, app_id, secret, sandbox, intents, enabled, self_id, username, created_at FROM bots WHERE id=?`, id).
		Scan(&b.ID, &b.Name, &b.AppID, &b.Secret, &b.Sandbox, &b.Intents, &b.Enabled, &b.SelfID, &b.Username, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) CreateBot(b *Bot) error {
	b.CreatedAt = now()
	if b.Intents == 0 {
		b.Intents = 1 << 25
	}
	res, err := s.db.Exec(`INSERT INTO bots(name, app_id, secret, sandbox, intents, enabled, created_at) VALUES(?,?,?,?,?,?,?)`,
		b.Name, strings.TrimSpace(b.AppID), b.Secret, b.Sandbox, b.Intents, b.Enabled, b.CreatedAt)
	if err != nil {
		return err
	}
	b.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateBot(b *Bot) error {
	res, err := s.db.Exec(`UPDATE bots SET name=?, app_id=?, secret=?, sandbox=?, intents=?, enabled=?, self_id=?, username=? WHERE id=?`,
		b.Name, strings.TrimSpace(b.AppID), b.Secret, b.Sandbox, b.Intents, b.Enabled, b.SelfID, b.Username, b.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetBotEnabled(id int64, enabled bool) error {
	res, err := s.db.Exec(`UPDATE bots SET enabled=? WHERE id=?`, enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetBotIdentity(id int64, selfID, username string) error {
	_, err := s.db.Exec(`UPDATE bots SET self_id=?, username=? WHERE id=?`, selfID, username, id)
	return err
}

func (s *Store) DeleteBot(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM listeners WHERE bot_id=?`,
		`DELETE FROM groups WHERE bot_id=?`,
		`DELETE FROM openid_map WHERE bot_id=?`,
		`DELETE FROM id_seq WHERE bot_id=?`,
		`DELETE FROM bots WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- listeners ----

func scanListener(rows interface{ Scan(...any) error }) (Listener, error) {
	var l Listener
	err := rows.Scan(&l.ID, &l.BotID, &l.Name, &l.Type, &l.Host, &l.Port, &l.Path, &l.URL, &l.PostURL, &l.Token, &l.Enabled, &l.CreatedAt)
	return l, err
}

func (s *Store) ListListeners() ([]Listener, error) {
	rows, err := s.db.Query(`SELECT id, bot_id, name, type, host, port, path, url, post_url, token, enabled, created_at FROM listeners ORDER BY bot_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listener
	for rows.Next() {
		l, err := scanListener(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetListener(id int64) (Listener, error) {
	l, err := scanListener(s.db.QueryRow(`SELECT id, bot_id, name, type, host, port, path, url, post_url, token, enabled, created_at FROM listeners WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

func (s *Store) CreateListener(l *Listener) error {
	l.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO listeners(bot_id, name, type, host, port, path, url, post_url, token, enabled, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		l.BotID, l.Name, l.Type, l.Host, l.Port, l.Path, l.URL, l.PostURL, l.Token, l.Enabled, l.CreatedAt)
	if err != nil {
		return err
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateListener(l *Listener) error {
	res, err := s.db.Exec(`UPDATE listeners SET bot_id=?, name=?, type=?, host=?, port=?, path=?, url=?, post_url=?, token=?, enabled=? WHERE id=?`,
		l.BotID, l.Name, l.Type, l.Host, l.Port, l.Path, l.URL, l.PostURL, l.Token, l.Enabled, l.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteListener(id int64) error {
	_, err := s.db.Exec(`DELETE FROM listeners WHERE id=?`, id)
	return err
}

// ---- groups ----

func (s *Store) ListGroups(botID int64) ([]Group, error) {
	q := `SELECT id, bot_id, open_id, num_id, name, last_seen, created_at FROM groups`
	args := []any{}
	if botID != 0 {
		q += ` WHERE bot_id=?`
		args = append(args, botID)
	}
	q += ` ORDER BY last_seen DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.BotID, &g.OpenID, &g.NumID, &g.Name, &g.LastSeen, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) nextID(botID int64, kind string, start int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var next int64
	err = tx.QueryRow(`SELECT next FROM id_seq WHERE bot_id=? AND kind=?`, botID, kind).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		next = start
	} else if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`INSERT INTO id_seq(bot_id, kind, next) VALUES(?,?,?) ON CONFLICT(bot_id, kind) DO UPDATE SET next=excluded.next`,
		botID, kind, next+1); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

// EnsureGroup returns the stable numeric group id for an OpenID, creating it if needed.
func (s *Store) EnsureGroup(botID int64, openID, name string) (Group, error) {
	var g Group
	err := s.db.QueryRow(`SELECT id, bot_id, open_id, num_id, name, last_seen, created_at FROM groups WHERE bot_id=? AND open_id=?`, botID, openID).
		Scan(&g.ID, &g.BotID, &g.OpenID, &g.NumID, &g.Name, &g.LastSeen, &g.CreatedAt)
	if err == nil {
		_, _ = s.db.Exec(`UPDATE groups SET last_seen=? WHERE id=?`, now(), g.ID)
		return g, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return g, err
	}
	var numID int64
	if n, perr := parseInt64(openID); perr == nil && n > 0 {
		numID = n
	} else {
		numID, err = s.nextID(botID, "group", 100000)
		if err != nil {
			return g, err
		}
	}
	g = Group{BotID: botID, OpenID: openID, NumID: numID, Name: name, LastSeen: now(), CreatedAt: now()}
	res, err := s.db.Exec(`INSERT INTO groups(bot_id, open_id, num_id, name, last_seen, created_at) VALUES(?,?,?,?,?,?)`,
		g.BotID, g.OpenID, g.NumID, g.Name, g.LastSeen, g.CreatedAt)
	if err != nil {
		return g, err
	}
	g.ID, _ = res.LastInsertId()
	return g, nil
}

func (s *Store) GroupByNum(botID, numID int64) (Group, error) {
	var g Group
	err := s.db.QueryRow(`SELECT id, bot_id, open_id, num_id, name, last_seen, created_at FROM groups WHERE bot_id=? AND num_id=?`, botID, numID).
		Scan(&g.ID, &g.BotID, &g.OpenID, &g.NumID, &g.Name, &g.LastSeen, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func (s *Store) GroupByOpenID(botID int64, openID string) (Group, error) {
	var g Group
	err := s.db.QueryRow(`SELECT id, bot_id, open_id, num_id, name, last_seen, created_at FROM groups WHERE bot_id=? AND open_id=?`, botID, openID).
		Scan(&g.ID, &g.BotID, &g.OpenID, &g.NumID, &g.Name, &g.LastSeen, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func (s *Store) SetGroupName(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE groups SET name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) DeleteGroup(id int64) error {
	_, err := s.db.Exec(`DELETE FROM groups WHERE id=?`, id)
	return err
}

// ---- openid mapping ----

// EnsureOpenID maps an OpenID string (e.g. member_openid / user_openid) onto a
// stable integer that OneBot events can carry.
func (s *Store) EnsureOpenID(botID int64, kind, openID string) (int64, error) {
	var numID int64
	err := s.db.QueryRow(`SELECT num_id FROM openid_map WHERE bot_id=? AND kind=? AND open_id=?`, botID, kind, openID).Scan(&numID)
	if err == nil {
		return numID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if n, perr := parseInt64(openID); perr == nil && n > 0 {
		// Prefer the real id when it is not already taken by another openid.
		// Probing first avoids leaving a hole in the generated-id sequence.
		var taken int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM openid_map WHERE bot_id=? AND kind=? AND num_id=?`, botID, kind, n).Scan(&taken)
		if taken == 0 {
			_, err = s.db.Exec(`INSERT INTO openid_map(bot_id, kind, open_id, num_id, created_at) VALUES(?,?,?,?,?)`,
				botID, kind, openID, n, now())
			if err == nil {
				return n, nil
			}
			if !strings.Contains(err.Error(), "UNIQUE") {
				return 0, err
			}
		}
	}
	numID, err = s.nextID(botID, kind, 200000)
	if err != nil {
		return 0, err
	}
	// The generated id could collide with a real numeric openid already mapped;
	// retry a few times before giving up.
	for i := 0; i < 50; i++ {
		_, err = s.db.Exec(`INSERT INTO openid_map(bot_id, kind, open_id, num_id, created_at) VALUES(?,?,?,?,?)`,
			botID, kind, openID, numID, now())
		if err == nil {
			return numID, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return 0, err
		}
		var taken int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM openid_map WHERE bot_id=? AND kind=? AND num_id=?`, botID, kind, numID).Scan(&taken)
		if taken == 0 {
			// Openid row itself may have been inserted concurrently; that's fine.
			var existing int64
			if e := s.db.QueryRow(`SELECT num_id FROM openid_map WHERE bot_id=? AND kind=? AND open_id=?`, botID, kind, openID).Scan(&existing); e == nil {
				return existing, nil
			}
			return 0, err
		}
		numID++
	}
	return 0, fmt.Errorf("无法为 %s 分配唯一 ID", openID)
}

func (s *Store) OpenIDByNum(botID int64, kind string, numID int64) (string, error) {
	var openID string
	err := s.db.QueryRow(`SELECT open_id FROM openid_map WHERE bot_id=? AND kind=? AND num_id=?`, botID, kind, numID).Scan(&openID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return openID, err
}

// ---- settings ----

// GetSetting returns a stored setting, or "" when it has never been written.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func parseInt64(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not numeric")
		}
		n = n*10 + int64(r-'0')
		if n < 0 {
			return 0, fmt.Errorf("overflow")
		}
	}
	return n, nil
}
