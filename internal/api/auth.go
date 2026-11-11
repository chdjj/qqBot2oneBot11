package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Auth guards the management API. The token is generated at startup, printed
// once on the terminal, and required for every /api/* request.
type Auth struct {
	mu     sync.RWMutex
	token  string
	source string // how the token was obtained, for the startup message
}

// NewAuth creates an authenticator from the given token, or generates one.
func NewAuth(token, source string) *Auth {
	if token == "" {
		token, source = generateToken(), "自动生成"
	}
	return &Auth{token: token, source: source}
}

func generateToken() string { return GenerateToken() }

// GenerateToken returns a fresh random admin token (base64url, 43 chars).
func GenerateToken() string {
	// 32 random bytes -> 43 chars base64url. Enough entropy for a local admin
	// secret; not meant to be a user-chosen password.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable for security purposes.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token returns the current token (used for the startup banner).
func (a *Auth) Token() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token
}

// Source describes where the token came from.
func (a *Auth) Source() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.source
}

// Presented extracts the token from a request. Accepted forms:
//
//	Authorization: Bearer <token>
//	Authorization: Token <token>
//	X-Admin-Token: <token>
//	?token=<token>   (needed by EventSource, which cannot send headers)
func presentedToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := cutPrefixFold(h, "bearer "); ok {
			return strings.TrimSpace(rest)
		}
		if rest, ok := cutPrefixFold(h, "token "); ok {
			return strings.TrimSpace(rest)
		}
		return strings.TrimSpace(h)
	}
	if v := r.Header.Get("X-Admin-Token"); v != "" {
		return v
	}
	return r.URL.Query().Get("token")
}

// cutPrefixFold is strings.CutPrefix but ignoring case in the prefix.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return s, false
	}
	if strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// Valid reports whether the request carries the correct token.
func (a *Auth) Valid(r *http.Request) bool {
	got := presentedToken(r)
	if got == "" {
		got = a.sessionToken(r)
	}
	if got == "" {
		return false
	}
	a.mu.RLock()
	want := a.token
	a.mu.RUnlock()
	// Constant-time compare to avoid timing leaks.
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Middleware rejects unauthenticated API requests. WebSocket handshakes and
// EventSource both arrive as plain GETs, so the ?token= form matters there.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, "未登录或 token 无效，请在控制台输入启动时终端打印的 token")
	})
}

// sessionCookie lets the browser keep working after the login page sets it,
// so WebSocket and EventSource connections from the same page authenticate
// without threading the token through every URL.
const sessionCookie = "qqbot_admin"

func (a *Auth) SetCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.Token(),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(12 * time.Hour),
	})
}

// sessionToken reads the cookie as a fallback credential.
func (a *Auth) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}
