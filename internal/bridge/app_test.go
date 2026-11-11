package bridge

import "testing"

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
