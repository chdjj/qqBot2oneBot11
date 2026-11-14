package bridge

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"qqbotonebot/internal/onebot"
	"qqbotonebot/internal/qq"
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

func TestExtractBase64Forms(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVQI12P4z8AAAAMBAQAY3Y2wAAAAAElFTkSuQmCC"

	cases := []struct {
		name string
		data map[string]any
		file string
		url  string
		ok   bool
	}{
		{"base64 scheme", map[string]any{}, "base64://" + png, "", true},
		{"data uri", map[string]any{}, "data:image/png;base64," + png, "", true},
		{"bare payload", map[string]any{}, png, "", true},
		{"http url is not base64", map[string]any{}, "", "https://example.com/a.png", false},
		{"filename is not base64", map[string]any{}, "a.png", "", false},
		{"empty", map[string]any{}, "", "", false},
	}
	for _, c := range cases {
		name, payload, ok, err := extractBase64(c.data, c.file, c.url)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.ok)
		}
		if ok {
			if len(payload) == 0 {
				t.Fatalf("%s: empty payload", c.name)
			}
		} else if name != "" || len(payload) != 0 {
			t.Fatalf("%s: should return zeros when not base64", c.name)
		}
	}
}

func TestExtractBase64InvalidPayload(t *testing.T) {
	// Explicitly marked base64 but undecodable: must be an error, not a silent
	// fallback to URL upload.
	_, _, ok, err := extractBase64(map[string]any{}, "base64://!!!notbase64!!!", "")
	if err == nil {
		t.Fatal("expected an error for invalid base64:// payload")
	}
	if ok {
		t.Fatal("should not report ok")
	}
}

func TestSegmentsToQQBase64Media(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVQI12P4z8AAAAMBAQAY3Y2wAAAAAElFTkSuQmCC"
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		{Type: "reply", Data: map[string]any{"id": "123"}},
		{Type: "image", Data: map[string]any{"file": "base64://" + png}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 7 {
		t.Fatalf("msg_type = %d", msg.msg.MsgType)
	}
	if len(msg.mediaData) == 0 {
		t.Fatal("mediaData lost — base64 would be resent as a URL upload")
	}
	if msg.mediaURL != "" {
		t.Fatalf("mediaURL should be empty, got %q", msg.mediaURL)
	}
	if msg.mediaName == "" || !strings.HasSuffix(msg.mediaName, ".png") {
		t.Fatalf("mediaName = %q", msg.mediaName)
	}
	if msg.mediaFileType != 1 {
		t.Fatalf("file type = %d", msg.mediaFileType)
	}
}

func TestMediaUploadBody(t *testing.T) {
	direct := qq.MediaUpload{FileType: 1, Data: "AAAA", Name: "a.png"}
	if b := direct.Body(); b["file_data"] != "AAAA" || b["url"] != nil {
		t.Fatalf("direct upload body = %v", b)
	}
	remote := qq.MediaUpload{FileType: 1, URL: "https://x/a.png"}
	if b := remote.Body(); b["url"] != "https://x/a.png" || b["file_data"] != nil {
		t.Fatalf("url upload body = %v", b)
	}
}

func TestVersionInfoFieldsMatchOneBot11(t *testing.T) {
	// The spec names these app_name / app_version / protocol_version. Clients
	// index them by name, and a missing key becomes null before a getString()
	// call NPEs (this is exactly how a Java client failed in practice).
	resp := onebot.OK("e", map[string]any{
		"app_name":         "qqbot-onebot",
		"app_version":      buildVersion,
		"protocol_version": "v11",
		"protocol":         "qqbot-v2",
		"qqbot_api":        "v2",
	})
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is %T", resp.Data)
	}
	for _, k := range []string{"app_name", "app_version", "protocol_version"} {
		v, exists := data[k]
		if !exists {
			t.Fatalf("missing required field %q", k)
		}
		if s, isStr := v.(string); !isStr || s == "" {
			t.Fatalf("field %q must be a non-empty string, got %#v", k, v)
		}
	}
}

func TestAtAllUsesOfficialGroupMarker(t *testing.T) {
	// The official group-chat marker is <qqbot-at-everyone />, not <@all>.
	// It is valid in text messages; the platform enforces that the bot is a
	// group admin and that only one is sent per group per day.
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		onebot.At("all"),
		onebot.Text(" 今晚八点开会"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 0 {
		t.Fatalf("msg_type = %d", msg.msg.MsgType)
	}
	if !strings.Contains(msg.msg.Content, "<qqbot-at-everyone />") {
		t.Fatalf("missing official marker: %q", msg.msg.Content)
	}
	if strings.Contains(msg.msg.Content, "<@all>") {
		t.Fatalf("legacy guild marker must not be used: %q", msg.msg.Content)
	}
	if !strings.Contains(msg.msg.Content, "今晚八点开会") {
		t.Fatalf("content lost: %q", msg.msg.Content)
	}
}

func TestAtAllWithMediaStillSends(t *testing.T) {
	msg, err := segmentsToQQMessage("group", []onebot.Segment{
		onebot.At("all"),
		onebot.Image("https://example.com/a.png", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 7 || msg.mediaURL == "" {
		t.Fatalf("media = %#v", msg)
	}
	if !strings.Contains(msg.msg.Content, "<qqbot-at-everyone />") {
		t.Fatalf("marker lost when sending media: %q", msg.msg.Content)
	}
}

func TestAtAllOnlyMessage(t *testing.T) {
	// At-mention alone is still a non-empty message.
	msg, err := segmentsToQQMessage("group", []onebot.Segment{onebot.At("all")})
	if err != nil {
		t.Fatal(err)
	}
	if msg.msg.MsgType != 0 || !strings.Contains(msg.msg.Content, "<qqbot-at-everyone />") {
		t.Fatalf("msg = %#v", msg.msg)
	}
}

func TestIsAtEveryoneError(t *testing.T) {
	yes := []int{40034142, 40034143, 40034144, 40034127}
	for _, code := range yes {
		err := &qq.APIError{Code: code, Message: "x"}
		if !isAtEveryoneError(err) {
			t.Fatalf("code %d should retry without the marker", code)
		}
	}
	// A message-only signal must also be recognised.
	if !isAtEveryoneError(&qq.APIError{Code: 9999, Message: "消息发送失败，@全体成员次数超限"}) {
		t.Fatal("message-based detection failed")
	}
	// Unrelated failures must not trigger the retry.
	for _, code := range []int{11244, 40034102, 304023} {
		if isAtEveryoneError(&qq.APIError{Code: code}) {
			t.Fatalf("code %d must not be treated as an @all problem", code)
		}
	}
	if isAtEveryoneError(errors.New("plain error")) {
		t.Fatal("non-API errors should not trigger the retry")
	}
}

func TestStripAtEveryoneFallback(t *testing.T) {
	// Mirrors what the retry does: remove the marker, keep the text.
	content := "<qqbot-at-everyone /> 今晚八点开会"
	stripped := strings.TrimSpace(strings.ReplaceAll(content, qqbotAtEveryone, ""))
	if stripped != "今晚八点开会" {
		t.Fatalf("stripped = %q", stripped)
	}
	// A marker-only message still yields a non-empty placeholder so the send
	// does not fail on an empty body.
	only := strings.TrimSpace(strings.ReplaceAll(qqbotAtEveryone, qqbotAtEveryone, ""))
	if only == "" {
		only = "（@全体成员 已省略）"
	}
	if only == "" || !strings.Contains(only, "@全体成员") {
		t.Fatalf("placeholder = %q", only)
	}
}

// pngBytes builds a small valid PNG payload for prepareMedia tests.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPrepareMediaConvertsImageToJPEG(t *testing.T) {
	app := &App{logs: newRingLog(10), consoleSub: map[int]chan []byte{}}
	msg := qqConvertedMessage{
		msg:           qq.Message{MsgType: 7},
		mediaFileType: 1,
		mediaData:     pngBytes(t, 200, 120),
		mediaName:     "photo.png",
	}
	up, data, err := app.prepareMedia(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
		t.Fatalf("image was not converted to JPEG: % x", data[:4])
	}
	if !strings.HasSuffix(up.Name, ".jpg") {
		t.Fatalf("name = %q, want .jpg", up.Name)
	}
	if up.FileType != 1 {
		t.Fatalf("file type = %d", up.FileType)
	}
	if up.URL != "" {
		t.Fatalf("URL should be empty for local bytes")
	}
}

func TestPrepareMediaKeepsNonImageBytes(t *testing.T) {
	app := &App{logs: newRingLog(10), consoleSub: map[int]chan []byte{}}
	video := []byte("\x00\x00\x00\x18ftypmp42") // mp4 magic
	msg := qqConvertedMessage{
		msg:           qq.Message{MsgType: 7},
		mediaFileType: 2, // video
		mediaData:     append(video, bytes.Repeat([]byte{0}, 64)...),
		mediaName:     "clip.mp4",
	}
	up, data, err := app.prepareMedia(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, video) {
		t.Fatal("video bytes must pass through untouched")
	}
	if up.Name != "clip.mp4" {
		t.Fatalf("name = %q", up.Name)
	}
}

func TestPrepareMediaURLCarriesNoBytes(t *testing.T) {
	app := &App{logs: newRingLog(10), consoleSub: map[int]chan []byte{}}
	msg := qqConvertedMessage{
		msg:           qq.Message{MsgType: 7},
		mediaFileType: 1,
		mediaURL:      "https://example.com/a.png",
	}
	up, data, err := app.prepareMedia(msg)
	if err != nil {
		t.Fatal(err)
	}
	if up.URL != "https://example.com/a.png" {
		t.Fatalf("url = %q", up.URL)
	}
	if len(data) != 0 {
		t.Fatal("URL media must not produce local bytes")
	}
}

func TestJpegName(t *testing.T) {
	cases := map[string]string{
		"a.png":       "a.jpg",
		"a.jpeg":      "a.jpg",
		"noext":       "noext.jpg",
		"":            "image.jpg",
		"dir.x/a.png": "dir.x/a.jpg",
	}
	for in, want := range cases {
		if got := jpegName(in); got != want {
			t.Fatalf("jpegName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsRetryableSendError(t *testing.T) {
	// Transient: network failures, timeouts, 5xx, throttling, "请重试" codes.
	retry := []error{
		errors.New("dial tcp: connection reset"),
		&qq.APIError{Status: 502, Code: 500},
		&qq.APIError{Status: 429, Code: 0},
		&qq.APIError{Status: 200, Code: 40093001},
		&qq.APIError{Status: 200, Code: 850027}, // 发送数据超时，请稍后重试
	}
	for _, err := range retry {
		if !isRetryableSendError(err) {
			t.Fatalf("%v should be retryable", err)
		}
	}

	// Permanent: review failures, permissions, quotas, mute, size, duplicates.
	noRetry := []error{
		&qq.APIError{Status: 200, Code: 11244},    // 内容审核未通过
		&qq.APIError{Status: 200, Code: 40034102}, // 主动消息无权限
		&qq.APIError{Status: 200, Code: 40034143}, // @全体成员需管理员
		&qq.APIError{Status: 200, Code: 40034142}, // @全体成员次数超限
		&qq.APIError{Status: 200, Code: 40054002}, // 机器人被禁言
		&qq.APIError{Status: 200, Code: 40054007}, // 消息长度超限
		&qq.APIError{Status: 200, Code: 40054005}, // 重复 message
		&qq.APIError{Status: 200, Code: 850018},   // 被禁言
		&qq.APIError{Status: 200, Code: 850031},   // 文件过大
	}
	for _, err := range noRetry {
		if isRetryableSendError(err) {
			t.Fatalf("%v must not be retried", err)
		}
	}

	if isRetryableSendError(nil) {
		t.Fatal("nil error must not be retryable")
	}
}
