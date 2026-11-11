package bridge

import (
	"sync"
	"time"
)

type LogEntry struct {
	Time  int64  `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type ringLog struct {
	mu    sync.Mutex
	items []LogEntry
	limit int
}

func newRingLog(limit int) *ringLog { return &ringLog{limit: limit} }

func (r *ringLog) add(e LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, e)
	if len(r.items) > r.limit {
		r.items = r.items[len(r.items)-r.limit:]
	}
}

func (r *ringLog) snapshot() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LogEntry, len(r.items))
	copy(out, r.items)
	return out
}

var _ = time.Now
