package flood

import (
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
)

func signed(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, content string, ts nostr.Timestamp, tags nostr.Tags) nostr.Event {
	t.Helper()
	evt := nostr.Event{Kind: kind, CreatedAt: ts, Content: content, Tags: tags}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return evt
}

func newStore() *slicestore.SliceStore {
	s := &slicestore.SliceStore{}
	s.Init()
	return s
}

func TestRepeatRules(t *testing.T) {
	store := newStore()
	g := NewGuard(store)
	alice, bob := nostr.Generate(), nostr.Generate()
	const t0 = nostr.Timestamp(1_800_000_000)

	first := signed(t, alice, 1, "Revuo Issue 201 is out", t0, nil)
	if g.Repeat(first) {
		t.Fatal("first post is not a repeat")
	}
	store.SaveEvent(first)

	cases := []struct {
		name string
		evt  nostr.Event
		want bool
	}{
		{"re-sending the same event", first, false},
		{"same text minutes later", signed(t, alice, 1, "Revuo Issue 201 is out", t0+120, nil), true},
		{"whitespace differences", signed(t, alice, 1, "  Revuo Issue 201 is out\n", t0+300, nil), true},
		{"backdated copy within the window", signed(t, alice, 1, "Revuo Issue 201 is out", t0-600, nil), true},
		{"different text", signed(t, alice, 1, "Revuo Issue 202 is out", t0+60, nil), false},
		{"another author", signed(t, bob, 1, "Revuo Issue 201 is out", t0+60, nil), false},
		{"a reply", signed(t, alice, 1, "Revuo Issue 201 is out", t0+60, nostr.Tags{{"e", nostr.ID{9}.Hex()}}), false},
		{"another kind", signed(t, alice, 1111, "Revuo Issue 201 is out", t0+60, nil), false},
	}
	for _, c := range cases {
		if got := g.Repeat(c.evt); got != c.want {
			t.Errorf("%s: Repeat = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoopStaysBlocked(t *testing.T) {
	store := newStore()
	g := NewGuard(store)
	bot := nostr.Generate()
	const t0 = nostr.Timestamp(1_800_000_000)

	kept := 0
	for i := range 30 { // every 10 minutes for 5 hours
		evt := signed(t, bot, 1, "Issue 201 is out", t0+nostr.Timestamp(i*600), nil)
		if !g.Repeat(evt) {
			store.SaveEvent(evt)
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("a continuous loop must keep only the first copy, kept %d", kept)
	}

	later := signed(t, bot, 1, "Issue 201 is out", t0+30*600+2*3600, nil)
	if g.Repeat(later) {
		t.Fatal("after a pause longer than the window the text may be posted again")
	}
}

func TestStoreCoversRestarts(t *testing.T) {
	store := newStore()
	alice := nostr.Generate()
	store.SaveEvent(signed(t, alice, 1, "hello", 1000, nil))

	// A fresh guard (e.g. after a restart) still sees what is stored. Small
	// timestamps also check that the window's lower bound does not underflow.
	if !NewGuard(store).Repeat(signed(t, alice, 1, "hello", 1060, nil)) {
		t.Fatal("stored notes must count after a restart")
	}
	gm := nostr.Tags{{"e", nostr.ID{1}.Hex()}}
	store.SaveEvent(signed(t, alice, 1, "Good morning!", 5000, gm))
	if NewGuard(store).Repeat(signed(t, alice, 1, "Good morning!", 5060, nil)) {
		t.Fatal("a top-level note must not be compared against replies")
	}
}
