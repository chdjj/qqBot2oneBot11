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

func TestSegmentsToQQMarkdown(t *testing.T) {
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		{Type: "markdown", Data: map[string]any{"content": "## 标题\n正文"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 2 {
		t.Fatalf("msg_type = %d", msg.msg.MsgType)
	}
	md, ok := msg.msg.Markdown.(map[string]any)
	if !ok || md["content"] != "## 标题\n正文" {
		t.Fatalf("markdown = %#v", msg.msg.Markdown)
	}
}

func TestSplitText(t *testing.T) {
	long := strings.Repeat("测", 9000)
	chunks := splitText(long, 4000)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		if len([]rune(c)) > 4000 {
			t.Fatalf("chunk too long: %d", len([]rune(c)))
		}
		total += len([]rune(c))
	}
	if total != 9000 {
		t.Fatalf("lost characters: %d", total)
	}
	// Prefers a newline break.
	s := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	got := splitText(s, 40)
	if len([]rune(got[0])) != 30 || got[1] != strings.Repeat("b", 30) {
		t.Fatalf("newline preference: %#v", got)
	}
	if splitText("short", 4000)[0] != "short" {
		t.Fatal("short text should not split")
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
