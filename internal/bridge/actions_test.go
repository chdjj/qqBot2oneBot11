package bridge

import (
	"strings"
	"testing"

	"qqbotonebot/internal/onebot"
)

func TestSegmentsToQQText(t *testing.T) {
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		onebot.Text("你好 "),
		onebot.At("1001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 0 {
		t.Fatalf("msg_type = %d", msg.msg.MsgType)
	}
	if !strings.Contains(msg.msg.Content, "你好") || !strings.Contains(msg.msg.Content, "@") {
		t.Fatalf("content = %q", msg.msg.Content)
	}
}

func TestSegmentsToQQMedia(t *testing.T) {
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		onebot.Image("https://example.com/a.png", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 7 || msg.mediaFileType != 1 || msg.mediaURL != "https://example.com/a.png" {
		t.Fatalf("media = %#v", msg)
	}
}

func TestSegmentsToQQRejects(t *testing.T) {
	cases := []struct {
		name string
		segs []onebot.Segment
		want string
	}{
		{"at-all", []onebot.Segment{onebot.At("all")}, "@全体"},
		{"no-url-media", []onebot.Segment{onebot.Image("", "local.png")}, "url"},
		{"two-media", []onebot.Segment{onebot.Image("u1", ""), onebot.Image("u2", "")}, "一个"},
		{"unsupported", []onebot.Segment{{Type: "json", Data: map[string]any{}}}, "不支持"},
		{"empty", []onebot.Segment{onebot.Text("  ")}, "为空"},
	}
	for _, c := range cases {
		_, err := segmentsToQQMessage("group", c.segs)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error %q missing %q", c.name, err.Error(), c.want)
		}
	}
}

func TestContentToSegments(t *testing.T) {
	segs := contentToSegments("你好<qqbot-at-1234> 世界")
	if len(segs) != 3 {
		t.Fatalf("got %d segments: %#v", len(segs), segs)
	}
	if segs[1].Type != "at" || segs[1].Data["qq"] != "1234" {
		t.Fatalf("at segment = %#v", segs[1])
	}
	// A bare '<' that is not a marker must survive.
	plain := contentToSegments("a < b")
	if onebot.PlainText(plain) != "a < b" {
		t.Fatalf("plain = %q", onebot.PlainText(plain))
	}
}

func TestMessageIDStable(t *testing.T) {
	if a, b := hashID("ROBOT1.0_abc"), hashID("ROBOT1.0_abc"); a != b {
		t.Fatal("hashID not deterministic")
	}
	if messageIDFromSeq("12345", "x") != 12345 {
		t.Fatal("numeric event id should be used as-is")
	}
	if messageIDFromSeq("EVT-1", "ROBOT1.0_x") <= 0 {
		t.Fatal("hashed id must be positive")
	}
}
