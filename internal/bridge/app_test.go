package bridge

import (
	"errors"
	"strings"
	"testing"

	"qqbotonebot/internal/onebot"
)

func TestSelfIDToInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"8888888", 8888888, true},
		// Real QQ bot ids are 20 digits and overflow int64.
		{"13080950038201073629", 3857578001346297821, true},
		{"", 0, false},
		{"abc", 0, false},
		{"0", 0, false},
	}
	for _, c := range cases {
		got, ok := selfIDToInt64(c.in)
		if ok != c.ok {
			t.Fatalf("%q ok=%v, want %v", c.in, ok, c.ok)
		}
		if ok && got != c.want {
			t.Fatalf("%q = %d, want %d", c.in, got, c.want)
		}
	}
	// Deterministic: same input must map to the same id every time.
	a, _ := selfIDToInt64("13080950038201073629")
	b, _ := selfIDToInt64("13080950038201073629")
	if a != b {
		t.Fatal("unstable mapping")
	}
	if a <= 0 {
		t.Fatalf("folded id must stay positive, got %d", a)
	}
}

func TestActionFromPath(t *testing.T) {
	cases := []struct{ path, base, want string }{
		{"/get_version_info", "", "get_version_info"},
		{"/send_group_msg", "", "send_group_msg"},
		{"/", "", ""},
		{"/api/v11/get_version_info", "/api/v11", "get_version_info"},
		{"/api/v11", "/api/v11", ""},
		{"/_events", "", ""},
		{"/get_version_info/", "", "get_version_info"},
		{"/a/b", "", ""},
	}
	for _, c := range cases {
		if got := actionFromPath(c.path, c.base); got != c.want {
			t.Fatalf("actionFromPath(%q,%q) = %q, want %q", c.path, c.base, got, c.want)
		}
	}
}

func TestTopLevelParams(t *testing.T) {
	raw := []byte(`{"group_id":"100000","message":[{"type":"text","data":{"text":"hi"}}],"action":"send_group_msg","echo":"x"}`)
	p := topLevelParams(raw)
	if p == nil {
		t.Fatal("expected params")
	}
	if p["group_id"] != "100000" {
		t.Fatalf("group_id = %#v", p["group_id"])
	}
	if _, ok := p["action"]; ok {
		t.Fatal("action must not leak into params")
	}
	if _, ok := p["echo"]; ok {
		t.Fatal("echo must not leak into params")
	}
	// An empty/param-less body must not fabricate params.
	if topLevelParams([]byte(`{"action":"get_version_info"}`)) != nil {
		t.Fatal("should return nil when there are no params")
	}
}

func TestIsParamName(t *testing.T) {
	for _, k := range []string{"access_token", "token", "echo"} {
		if isParamName(k) {
			t.Fatalf("%q should be excluded", k)
		}
	}
	if !isParamName("group_id") {
		t.Fatal("group_id should be a param")
	}
}

func TestStatusCodeToRetCode(t *testing.T) {
	cases := map[int]int{400: 1400, 401: 1401, 403: 1403, 404: 1404}
	for status, want := range cases {
		if got := statusCodeToRetCode(status); got != want {
			t.Fatalf("%d -> %d, want %d", status, got, want)
		}
	}
}

func TestWSRetCodeForDispatchErrors(t *testing.T) {
	if got := wsRetCode(&onebot.ErrActionNotFound{Action: "x"}); got != 1404 {
		t.Fatalf("not found -> %d, want 1404", got)
	}
	if got := wsRetCode(&onebot.ErrUnsupported{Action: "x"}); got != 1403 {
		t.Fatalf("unsupported -> %d, want 1403", got)
	}
	if got := wsRetCode(errors.New("other")); got != 1400 {
		t.Fatalf("other -> %d, want 1400", got)
	}
}

func TestRedactInlinePayloads(t *testing.T) {
	payload := strings.Repeat("QUJD", 500) // 2000 base64 chars
	in := `{"message":[{"file":"base64://` + payload + `"}]}`
	out := redactInlinePayloads(in)
	if len(out) > 300 {
		t.Fatalf("redacted output still %d chars", len(out))
	}
	if !strings.Contains(out, "base64 已省略") {
		t.Fatalf("missing marker: %q", out)
	}
	// Structure must survive so the log stays readable.
	if !strings.HasPrefix(out, `{"message":[{"file":"base64://`) || !strings.HasSuffix(out, `"}]}`) {
		t.Fatalf("structure damaged: %q", out)
	}
	// Data URIs get the same treatment.
	d := `{"f":"data:image/png;base64,` + payload + `"}`
	if o := redactInlinePayloads(d); strings.Contains(o, payload) {
		t.Fatal("data URI payload not redacted")
	}
	// Plain parameters are left alone.
	if o := redactInlinePayloads(`{"group_id":"100000"}`); o != `{"group_id":"100000"}` {
		t.Fatalf("innocent params changed: %q", o)
	}
}
