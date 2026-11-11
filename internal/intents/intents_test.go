package intents

import "testing"

func TestMaskAndBits(t *testing.T) {
	m := Mask([]uint{25, 30})
	want := int64(1<<25 | 1<<30)
	if m != want {
		t.Fatalf("Mask = %d, want %d", m, want)
	}
	bits := Bits(m)
	if len(bits) != 2 || bits[0] != 25 || bits[1] != 30 {
		t.Fatalf("Bits = %v", bits)
	}
}

func TestMaskRoundTrip(t *testing.T) {
	for _, g := range All {
		m := Mask([]uint{g.Bit})
		back := Bits(m)
		if len(back) != 1 || back[0] != g.Bit {
			t.Fatalf("bit %d round trip -> %v", g.Bit, back)
		}
	}
}

func TestDefaultMask(t *testing.T) {
	if got := DefaultMask(); got != 1<<25 {
		t.Fatalf("DefaultMask = %d, want %d", got, int64(1<<25))
	}
	// Exactly one default keeps "new bot works out of the box" unambiguous.
	count := 0
	for _, g := range All {
		if g.Default {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d defaults, want 1", count)
	}
}

func TestUnknownBits(t *testing.T) {
	// Bit 5 is not documented; it must be reported so the console can warn.
	if u := UnknownBits(1<<25 | 1<<5); u != 1<<5 {
		t.Fatalf("UnknownBits = %d, want %d", u, int64(1<<5))
	}
	if u := UnknownBits(1 << 25); u != 0 {
		t.Fatalf("UnknownBits = %d, want 0", u)
	}
}

func TestNoDuplicateBits(t *testing.T) {
	seen := map[uint]bool{}
	for _, g := range All {
		if seen[g.Bit] {
			t.Fatalf("duplicate bit %d (%s)", g.Bit, g.Name)
		}
		seen[g.Bit] = true
		if g.Name == "" || g.Label == "" {
			t.Fatalf("bit %d missing name/label", g.Bit)
		}
	}
}
