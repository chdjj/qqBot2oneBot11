package qq

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// uploadMock stands in for the QQ upstream during a chunked upload.
type uploadMock struct {
	mu        sync.Mutex
	prepared  map[string]any
	parts     []map[string]any
	merged    map[string]any
	putSizes  []int
	putBodies [][]byte
	failPart  int // index that should fail once, -1 to disable
}

func (m *uploadMock) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/groups/GOPEN/upload_prepare", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.prepared = body
		m.mu.Unlock()

		// Three 4-byte parts so tests can use tiny payloads.
		parts := []map[string]any{}
		for i := 0; i < 3; i++ {
			parts = append(parts, map[string]any{
				"index": i, "block_size": "4",
				"presigned_url": "http://" + r.Host + "/put/" + string(rune('0'+i)),
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"upload_id":  "up-1",
			"block_size": "4",
			"parts":      parts,
			"upload_config": map[string]any{
				"concurrency": 1, "retry_timeout": 30, "retry_delay": 0,
			},
		})
	})
	mux.HandleFunc("/put/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		idx := int(r.URL.Path[len(r.URL.Path)-1] - '0')
		m.mu.Lock()
		m.putSizes = append(m.putSizes, len(body))
		m.putBodies = append(m.putBodies, body)
		m.mu.Unlock()
		if m.failPart == idx {
			m.mu.Lock()
			m.failPart = -1 // fail only the first attempt
			m.mu.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/groups/GOPEN/upload_part_finish", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.parts = append(m.parts, body)
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{})
	})
	mux.HandleFunc("/v2/groups/GOPEN/files", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.merged = body
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"file_info": "INFO_FROM_MERGE", "ttl": 3600,
		})
	})
	return mux
}

func newUploadClient(server *httptest.Server) *Client {
	c := NewClient("app", "secret", false)
	c.SetBaseURL(server.URL)
	// Skip the token endpoint: return a static token.
	c.forceToken("test-token")
	return c
}

func TestUploadChunkedHappyPath(t *testing.T) {
	m := &uploadMock{failPart: -1}
	server := httptest.NewServer(m.handler(t))
	defer server.Close()
	c := newUploadClient(server)

	data := []byte("0123456789ab") // 12 bytes -> three 4-byte parts
	up, err := c.UploadChunked(context.Background(), "group", "GOPEN", MediaUpload{
		FileType: 1, Name: "big.png",
	}, data)
	if err != nil {
		t.Fatal(err)
	}
	if up.FileInfo != "INFO_FROM_MERGE" {
		t.Fatalf("file_info = %q", up.FileInfo)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.prepared["file_size"] != "12" {
		t.Fatalf("file_size = %v", m.prepared["file_size"])
	}
	if m.prepared["file_name"] != "big.png" {
		t.Fatalf("file_name = %v", m.prepared["file_name"])
	}
	// The digests must be the real ones.
	sum := md5.Sum(data)
	if m.prepared["md5"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("md5 = %v", m.prepared["md5"])
	}
	if len(m.prepared["sha1"].(string)) != 40 {
		t.Fatalf("sha1 = %v", m.prepared["sha1"])
	}
	if len(m.prepared["md5_10m"].(string)) != 32 {
		t.Fatalf("md5_10m = %v", m.prepared["md5_10m"])
	}

	if len(m.parts) != 3 {
		t.Fatalf("confirmed %d parts, want 3", len(m.parts))
	}
	for i, p := range m.parts {
		if p["part_index"].(float64) != float64(i) {
			t.Fatalf("part %d index = %v", i, p["part_index"])
		}
		if p["upload_id"] != "up-1" {
			t.Fatalf("part %d upload_id = %v", i, p["upload_id"])
		}
	}
	// Every chunk must have been PUT with its own bytes.
	if len(m.putBodies) < 3 {
		t.Fatalf("expected >=3 PUTs, got %d", len(m.putBodies))
	}
	if string(m.putBodies[0]) != "0123" || string(m.putBodies[2]) != "89ab" {
		t.Fatalf("chunks assembled wrongly: %q .. %q", m.putBodies[0], m.putBodies[2])
	}
	if m.merged["upload_id"] != "up-1" {
		t.Fatalf("merge upload_id = %v", m.merged["upload_id"])
	}
	// The merge call must not carry a url.
	if _, has := m.merged["url"]; has {
		t.Fatal("merge request must not include url")
	}
}

func TestUploadChunkedRetriesFailedPart(t *testing.T) {
	m := &uploadMock{failPart: 1}
	server := httptest.NewServer(m.handler(t))
	defer server.Close()
	c := newUploadClient(server)

	data := []byte("0123456789ab")
	if _, err := c.UploadChunked(context.Background(), "group", "GOPEN", MediaUpload{
		FileType: 1, Name: "x.png",
	}, data); err != nil {
		t.Fatalf("a single failed part should be retried: %v", err)
	}
}

func TestUploadChunkedRejectsEmpty(t *testing.T) {
	c := NewClient("a", "b", false)
	if _, err := c.UploadChunked(context.Background(), "group", "G", MediaUpload{}, nil); err == nil {
		t.Fatal("empty payload must be rejected")
	}
}

func TestSliceForPart(t *testing.T) {
	data := []byte("abcdefghij")
	cases := []struct {
		index, size int
		want        string
	}{
		{0, 4, "abcd"},
		{1, 4, "efgh"},
		{2, 4, "ij"},
	}
	for _, c := range cases {
		got, ok := sliceForPart(data, UploadPart{Index: c.index, BlockSize: itoa(c.size)}, 4)
		if !ok || string(got) != c.want {
			t.Fatalf("part %d = %q (ok=%v), want %q", c.index, got, ok, c.want)
		}
	}
	// Out of range indices are rejected rather than silently clamped.
	if _, ok := sliceForPart(data, UploadPart{Index: 5, BlockSize: "4"}, 4); ok {
		t.Fatal("out-of-range part must not be ok")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b strings.Builder
	for n > 0 {
		b.WriteByte(byte('0' + n%10))
		n /= 10
	}
	// reverse
	r := []rune(strings.TrimSpace(b.String()))
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
