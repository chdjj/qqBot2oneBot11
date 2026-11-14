package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
	"qqbotonebot/internal/store"
)

// retryServer fails the first N send calls with the given status, then succeeds.
func retryServer(failures int32, status int, code int) *httptest.Server {
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= failures {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{
				"code": code, "message": "transient failure", "err_code": code,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "MSG-OK"})
	}))
}

func newTestAppWithClient(t *testing.T, srv *httptest.Server) (*App, *Bot) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	app := NewApp(st)

	c := qq.NewClient("app", "secret", false)
	c.SetBaseURL(srv.URL)
	c.SetTokenForTest("test-token")

	b := &Bot{
		rec:    store.Bot{ID: 1, AppID: "app"},
		client: c,
		recent: map[string]*RecentIncoming{},
		sent:   map[int64]*SentMessage{},
	}
	return app, b
}

func TestSendWithRetrySucceedsAfterTransientFailure(t *testing.T) {
	srv := retryServer(1, http.StatusBadGateway, 500)
	defer srv.Close()
	app, b := newTestAppWithClient(t, srv)

	res, err := app.sendWithRetry(context.Background(), b, "group", "GID",
		&qq.Message{MsgType: 0, Content: "hi"}, "测试")
	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
	if res.ID != "MSG-OK" {
		t.Fatalf("id = %q", res.ID)
	}
}

func TestSendWithRetryGivesUpAfterOneRetry(t *testing.T) {
	srv := retryServer(5, http.StatusInternalServerError, 500)
	defer srv.Close()
	app, b := newTestAppWithClient(t, srv)

	_, err := app.sendWithRetry(context.Background(), b, "group", "GID",
		&qq.Message{MsgType: 0, Content: "hi"}, "测试")
	if err == nil {
		t.Fatal("expected failure after a single retry")
	}
}

func TestSendWithRetryDoesNotRetryPermanentError(t *testing.T) {
	srv := retryServer(1, http.StatusOK, 11244) // content review failure
	defer srv.Close()
	app, b := newTestAppWithClient(t, srv)

	_, err := app.sendWithRetry(context.Background(), b, "group", "GID",
		&qq.Message{MsgType: 0, Content: "hi"}, "测试")
	if err == nil {
		t.Fatal("expected the permanent error to surface")
	}
	var ae *qq.APIError
	if !asAPIError(err, &ae) || ae.Code != 11244 {
		t.Fatalf("err = %v", err)
	}
}

func asAPIError(err error, target **qq.APIError) bool {
	return errors.As(err, target)
}

func TestActiveSendHasNoReplyMarker(t *testing.T) {
	// Without an explicit reply segment or msg_id, the outgoing message must be
	// an ordinary active message: no msg_id, no msg_seq.
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		onebot.Text("定时提醒：该打卡了"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.replyTo != "" {
		t.Fatalf("replyTo = %q, want empty for an active send", msg.replyTo)
	}
}

func TestReplySegmentMakesSendPassive(t *testing.T) {
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		{Type: "reply", Data: map[string]any{"id": "EVT-123"}},
		onebot.Text("收到"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.replyTo != "EVT-123" {
		t.Fatalf("replyTo = %q, want EVT-123", msg.replyTo)
	}
}

func TestDispatchMessageWithoutExplicitIDIsActive(t *testing.T) {
	// Seed a recent inbound event so the old behaviour would have made this
	// passive. It must still go out active.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"msg_id"`) {
			t.Errorf("active send leaked a msg_id: %s", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "MSG-1"})
	}))
	defer srv.Close()

	app, b := newTestAppWithClient(t, srv)
	b.rememberIncoming("group:GID", "EVT-OLD", 1, "u", 1)

	resp := app.dispatchMessage(context.Background(), b, nil, "group", "GID", 1,
		[]onebot.Segment{onebot.Text("主动推送")}, 0, "")
	if resp.Status != "ok" {
		t.Fatalf("status = %s msg=%s", resp.Status, resp.Msg)
	}
}
