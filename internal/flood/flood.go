package flood

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
)

// Window is how far apart two identical top-level notes must be. A bridge or
// bot stuck in a loop republishes the same text minutes apart; people rarely
// repeat a top-level post within an hour, while identical replies are common.
const Window = time.Hour

const (
	maxCompared = 500
	maxTracked  = 100_000
)

// Guard rejects top-level notes that repeat the same author's text within
// Window of the last time that text was seen, including rejected copies. A
// loop that keeps reposting therefore stays blocked until it pauses for longer
// than Window, instead of getting one copy through per Window.
type Guard struct {
	events eventstore.Store

	mu   sync.Mutex
	seen map[[32]byte]sighting
}

type sighting struct {
	id nostr.ID
	at nostr.Timestamp
}

func NewGuard(events eventstore.Store) *Guard {
	return &Guard{events: events, seen: map[[32]byte]sighting{}}
}

// Repeat reports whether evt repeats a recent note and records it either way.
// Re-sending the very same event is not a repeat; stores report that as a duplicate.
func (g *Guard) Repeat(evt nostr.Event) bool {
	if !topLevelNote(evt) {
		return false
	}
	key := sha256.Sum256(append(evt.PubKey[:], strings.TrimSpace(evt.Content)...))

	g.mu.Lock()
	last, ok := g.seen[key]
	repeat := ok && last.id != evt.ID && within(last.at, evt.CreatedAt)
	if !ok || evt.CreatedAt > last.at {
		g.seen[key] = sighting{id: evt.ID, at: evt.CreatedAt}
	}
	if len(g.seen) > maxTracked {
		g.prune(evt.CreatedAt)
	}
	g.mu.Unlock()

	return repeat || storedRepeat(g.events, evt)
}

func (g *Guard) prune(now nostr.Timestamp) {
	for k, s := range g.seen {
		if !within(s.at, now) {
			delete(g.seen, k)
		}
	}
	if len(g.seen) > maxTracked {
		clear(g.seen)
	}
}

// storedRepeat covers what the in-memory view has not seen, e.g. after a restart.
func storedRepeat(events eventstore.Store, evt nostr.Event) bool {
	content := strings.TrimSpace(evt.Content)
	window := nostr.Timestamp(Window.Seconds())
	filter := nostr.Filter{
		Authors: []nostr.PubKey{evt.PubKey},
		Kinds:   []nostr.Kind{nostr.KindTextNote},
		// Stores convert bounds to unsigned integers, so a negative Since would wrap.
		Since: max(evt.CreatedAt-window, 0),
		Until: evt.CreatedAt + window,
		Limit: maxCompared,
	}
	for prev := range events.QueryEvents(filter, maxCompared) {
		if prev.ID != evt.ID && topLevelNote(prev) && strings.TrimSpace(prev.Content) == content {
			return true
		}
	}
	return false
}

func topLevelNote(evt nostr.Event) bool {
	return evt.Kind == nostr.KindTextNote && evt.Tags.Find("e") == nil
}

func within(a, b nostr.Timestamp) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < nostr.Timestamp(Window.Seconds())
}
