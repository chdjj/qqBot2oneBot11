package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBotCRUD(t *testing.T) {
	s := newTestStore(t)
	b := Bot{Name: "A", AppID: "app1", Secret: "sec1", Enabled: true}
	if err := s.CreateBot(&b); err != nil {
		t.Fatal(err)
	}
	if b.ID == 0 || b.Intents != 1<<25 {
		t.Fatalf("defaults not applied: %#v", b)
	}
	got, err := s.GetBot(b.ID)
	if err != nil || got.AppID != "app1" {
		t.Fatalf("get: %#v %v", got, err)
	}
	// Duplicate AppID must fail (UNIQUE constraint).
	dup := Bot{Name: "B", AppID: "app1", Secret: "x"}
	if err := s.CreateBot(&dup); err == nil {
		t.Fatal("duplicate AppID should be rejected")
	}
	if err := s.SetBotIdentity(b.ID, "999", "Nick"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBot(b.ID)
	if got.SelfID != "999" || got.Username != "Nick" {
		t.Fatalf("identity not saved: %#v", got)
	}
	if err := s.SetBotEnabled(b.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetBot(b.ID); got.Enabled {
		t.Fatal("enabled should be false")
	}
}

func TestEnsureGroupStableIDs(t *testing.T) {
	s := newTestStore(t)
	g1, err := s.EnsureGroup(1, "A1B2C3", "")
	if err != nil || g1.NumID != 100000 {
		t.Fatalf("first group: %#v %v", g1, err)
	}
	// Same openid returns the same numeric id.
	g1b, _ := s.EnsureGroup(1, "A1B2C3", "")
	if g1b.NumID != g1.NumID {
		t.Fatalf("id not stable: %d vs %d", g1.NumID, g1b.NumID)
	}
	// A numeric openid is used verbatim.
	g2, _ := s.EnsureGroup(1, "123456789", "")
	if g2.NumID != 123456789 {
		t.Fatalf("numeric openid should be reused: %d", g2.NumID)
	}
	// Different bots get independent mappings.
	g3, _ := s.EnsureGroup(2, "A1B2C3", "")
	if g3.NumID != 100000 {
		t.Fatalf("per-bot numbering: %d", g3.NumID)
	}
	if _, err := s.GroupByNum(2, g1.NumID); err != nil {
		t.Fatalf("group by num on bot 2: %v", err)
	}
	if _, err := s.GroupByNum(1, g3.NumID+1); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestEnsureOpenIDMapping(t *testing.T) {
	s := newTestStore(t)
	n1, _ := s.EnsureOpenID(1, "member", "MEM_open_1")
	n2, _ := s.EnsureOpenID(1, "member", "MEM_open_2")
	if n1 == n2 {
		t.Fatal("distinct openids must not collide")
	}
	if n1 != 200000 {
		t.Fatalf("first member id = %d", n1)
	}
	back, err := s.OpenIDByNum(1, "member", n1)
	if err != nil || back != "MEM_open_1" {
		t.Fatalf("reverse lookup: %q %v", back, err)
	}
	// A numeric openid whose value is free is reused verbatim.
	numOpen, _ := s.EnsureOpenID(1, "member", "300001")
	if numOpen != 300001 {
		t.Fatalf("numeric openid: %d", numOpen)
	}
	// A numeric openid that collides with an existing mapping is not silently
	// merged onto it; it gets its own generated id instead.
	collide, err := s.EnsureOpenID(1, "member", "200001")
	if err != nil {
		t.Fatal(err)
	}
	if collide == 200001 {
		t.Fatalf("colliding openid shares n2's id: %d", collide)
	}
	back, _ = s.OpenIDByNum(1, "member", 200001)
	if back != "MEM_open_2" {
		t.Fatalf("existing mapping was overwritten: %q", back)
	}
	// The next generated id must avoid all of the above.
	n3, err := s.EnsureOpenID(1, "member", "MEM_open_3")
	if err != nil {
		t.Fatal(err)
	}
	if n3 == numOpen || n3 == n1 || n3 == n2 || n3 == collide {
		t.Fatalf("generated id collided: %d", n3)
	}
}

func TestListenerCRUDAndDeleteBot(t *testing.T) {
	s := newTestStore(t)
	b := Bot{Name: "A", AppID: "a1", Secret: "s"}
	_ = s.CreateBot(&b)
	l := Listener{BotID: b.ID, Name: "ws", Type: ListenerWSServer, Port: 8081, Enabled: true}
	if err := s.CreateListener(&l); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListListeners()
	if len(list) != 1 || list[0].Port != 8081 {
		t.Fatalf("list: %#v", list)
	}
	if err := s.UpdateListener(&l); err != nil {
		t.Fatal(err)
	}
	// Deleting the bot cascades its listeners and groups.
	_, _ = s.EnsureGroup(b.ID, "G_open", "")
	if err := s.DeleteBot(b.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListListeners(); len(list) != 0 {
		t.Fatalf("listeners not removed: %#v", list)
	}
	if gs, _ := s.ListGroups(b.ID); len(gs) != 0 {
		t.Fatalf("groups not removed: %#v", gs)
	}
}

func TestNextIDIncrements(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.nextID(1, "group", 100000)
	b, _ := s.nextID(1, "group", 100000)
	if b != a+1 {
		t.Fatalf("nextID not incrementing: %d then %d", a, b)
	}
}
