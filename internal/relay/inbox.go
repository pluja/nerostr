package relay

import (
	"context"
	"iter"
	"slices"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
)

// maxInboxRecipients stops a non-member from reaching every member with a single event.
const maxInboxRecipients = 20

// memberCacheTTL bounds how stale the read-side membership view can be. Writes
// always check the database directly.
const memberCacheTTL = 30 * time.Second

// isInbox reports whether a non-member event is addressed to at least one member.
func (r *Relay) isInbox(ctx context.Context, evt nostr.Event) bool {
	if !slices.Contains(r.cfg.InboxKinds, evt.Kind) {
		return false
	}
	var recipients []nostr.PubKey
	for tag := range evt.Tags.FindAll("p") {
		if pk, err := nostr.PubKeyFromHexCheap(tag[1]); err == nil {
			recipients = append(recipients, pk)
		}
	}
	if len(recipients) == 0 || len(recipients) > maxInboxRecipients {
		return false
	}
	for _, pk := range recipients {
		if member, err := r.store.IsMember(ctx, pk); err == nil && member {
			return true
		}
	}
	return false
}

// targeted reports whether a filter asks for specific events, people or
// threads, as opposed to browsing a feed. Only targeted queries see events
// from non-members, so inbox traffic never shows up in the relay's global feed.
func targeted(f nostr.Filter) bool {
	if len(f.IDs) > 0 || len(f.Authors) > 0 {
		return true
	}
	for _, name := range []string{"p", "P", "e", "E", "a", "A", "q"} {
		if len(f.Tags[name]) > 0 {
			return true
		}
	}
	return false
}

func (r *Relay) membersOnly(base func(context.Context, nostr.Filter) iter.Seq[nostr.Event]) func(context.Context, nostr.Filter) iter.Seq[nostr.Event] {
	return func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		if targeted(filter) || khatru.IsInternalCall(ctx) {
			return base(ctx, filter)
		}
		return func(yield func(nostr.Event) bool) {
			for evt := range base(ctx, filter) {
				if r.members.is(ctx, evt.PubKey) && !yield(evt) {
					return
				}
			}
		}
	}
}

func (r *Relay) preventFeedBroadcast(_ *khatru.WebSocket, filter nostr.Filter, evt nostr.Event) bool {
	return !targeted(filter) && !r.members.is(context.Background(), evt.PubKey)
}

type memberCache struct {
	lookup func(context.Context, nostr.PubKey) (bool, error)
	mu     sync.Mutex
	items  map[nostr.PubKey]cachedMember
}

type cachedMember struct {
	member bool
	at     time.Time
}

func newMemberCache(lookup func(context.Context, nostr.PubKey) (bool, error)) *memberCache {
	return &memberCache{lookup: lookup, items: map[nostr.PubKey]cachedMember{}}
}

func (c *memberCache) is(ctx context.Context, pk nostr.PubKey) bool {
	now := time.Now()
	c.mu.Lock()
	item, ok := c.items[pk]
	c.mu.Unlock()
	if ok && now.Sub(item.at) < memberCacheTTL {
		return item.member
	}
	member, err := c.lookup(ctx, pk)
	if err != nil {
		return false
	}
	c.mu.Lock()
	if len(c.items) > 50_000 {
		clear(c.items)
	}
	c.items[pk] = cachedMember{member: member, at: now}
	c.mu.Unlock()
	return member
}
