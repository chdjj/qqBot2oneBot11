package onebot

import (
	"reflect"
	"testing"
)

func TestParseCQRoundTrip(t *testing.T) {
	in := "你好[CQ:at,qq=12345]看这个[CQ:image,url=https://a.example/x.png]"
	segs := ParseCQ(in)
	if len(segs) != 4 {
		t.Fatalf("got %d segments, want 4", len(segs))
	}
	if segs[0].Type != "text" || segs[0].Data["text"] != "你好" {
		t.Fatalf("first segment = %#v", segs[0])
	}
	if segs[1].Type != "at" || segs[1].Data["qq"] != "12345" {
		t.Fatalf("second = %#v", segs[1])
	}
	if segs[2].Type != "text" || segs[2].Data["text"] != "看这个" {
		t.Fatalf("third = %#v", segs[2])
	}
	if segs[3].Type != "image" || segs[3].Data["url"] != "https://a.example/x.png" {
		t.Fatalf("fourth = %#v", segs[3])
	}
	out := ToCQ(segs)
	if out != in {
		t.Fatalf("round trip = %q, want %q", out, in)
	}
}

func TestCQEscape(t *testing.T) {
	segs := []Segment{Text("a,b&c[d]")}
	got := ToCQ(segs)
	want := "a&#44;b&#38;c&#91;d&#93;"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	back := ParseCQ(got)
	if len(back) != 1 || back[0].Data["text"] != "a,b&c[d]" {
		t.Fatalf("unescape failed: %#v", back)
	}
}

func TestParseSegmentsShapes(t *testing.T) {
	// String form.
	segs, err := ParseSegments("hi")
	if err != nil || len(segs) != 1 || segs[0].Type != "text" {
		t.Fatalf("string form: %#v %v", segs, err)
	}
	// Array-of-maps as JSON decoding produces.
	raw := []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "a"}},
		map[string]any{"type": "at", "data": map[string]any{"qq": float64(42)}},
	}
	segs, err = ParseSegments(raw)
	if err != nil || len(segs) != 2 {
		t.Fatalf("array form: %#v %v", segs, err)
	}
	if segs[1].Data["qq"] != int64(42) {
		t.Fatalf("integer normalization failed: %#v", segs[1].Data)
	}
	if _, err := ParseSegments(42); err == nil {
		t.Fatal("expected error for numeric message")
	}
}

func TestPlainText(t *testing.T) {
	segs := []Segment{
		At("123"),
		Text(" 你好"),
		Face("1"),
		Image("u", "f"),
	}
	got := PlainText(segs)
	want := "@123  你好[表情][图片]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParamHelpers(t *testing.T) {
	p := map[string]any{
		"group_id":     float64(123456),
		"user_id":      "789",
		"message_type": "group",
		"auto_escape":  true,
	}
	if got := IntParam(p, "group_id"); got != 123456 {
		t.Fatalf("group_id = %d", got)
	}
	if got := IntParam(p, "user_id"); got != 789 {
		t.Fatalf("user_id = %d", got)
	}
	if got := StrParam(p, "message_type"); got != "group" {
		t.Fatalf("message_type = %q", got)
	}
	if !BoolParam(p, "auto_escape") {
		t.Fatal("auto_escape should be true")
	}
}

func TestAPIResponseJSON(t *testing.T) {
	r := OK("e1", map[string]any{"message_id": int64(7)})
	if r.Status != "ok" || r.RetCode != 0 || r.Echo != "e1" {
		t.Fatalf("bad response: %#v", r)
	}
	f := Failed("e2", 120, "nope")
	if f.Status != "failed" || f.RetCode != 120 || f.Msg != "nope" {
		t.Fatalf("bad failure: %#v", f)
	}
	if !reflect.DeepEqual(f.Data, nil) {
		t.Fatalf("data should be nil")
	}
}
