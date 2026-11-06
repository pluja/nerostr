package alerts

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip59"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestMonitor(t *testing.T) {
	var beats atomic.Int32
	hb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { beats.Add(1) }))
	defer hb.Close()

	var sent []string
	problem := ""
	clock := time.Unix(1_800_000_000, 0)
	m := &Monitor{
		Name:         "relay.example.com",
		Problem:      func() string { return problem },
		Notify:       func(_ context.Context, text string) { sent = append(sent, text) },
		After:        10 * time.Minute,
		HeartbeatURL: hb.URL,
		Heartbeat:    5 * time.Minute,
		Log:          discard,
		now:          func() time.Time { return clock },
		client:       hb.Client(),
	}
	ctx := context.Background()
	advance := func(d time.Duration) { clock = clock.Add(d); m.step(ctx) }

	m.step(ctx)
	if beats.Load() != 1 {
		t.Fatalf("healthy start should ping the heartbeat, got %d", beats.Load())
	}
	advance(time.Minute)
	if beats.Load() != 1 {
		t.Fatal("heartbeat must respect its interval")
	}

	problem = "wallet stuck"
	advance(time.Minute)
	advance(5 * time.Minute)
	if len(sent) != 0 {
		t.Fatalf("alerted too early: %v", sent)
	}
	advance(5 * time.Minute)
	if len(sent) != 1 || !strings.Contains(sent[0], "wallet stuck") || !strings.Contains(sent[0], "relay.example.com") {
		t.Fatalf("expected one alert, got %v", sent)
	}
	advance(30 * time.Minute)
	if len(sent) != 1 {
		t.Fatal("must not repeat the alert while the problem persists")
	}
	if beats.Load() != 1 {
		t.Fatal("no heartbeats while unhealthy")
	}

	problem = ""
	advance(time.Minute)
	if len(sent) != 2 || !strings.Contains(sent[1], "working again") {
		t.Fatalf("expected recovery message, got %v", sent)
	}
	if beats.Load() != 2 {
		t.Fatal("heartbeat should resume after recovery")
	}

	problem = "blip"
	advance(time.Minute)
	problem = ""
	advance(time.Minute)
	if len(sent) != 2 {
		t.Fatalf("short blips must not alert: %v", sent)
	}
}

func TestNotifierDeliversReadableDM(t *testing.T) {
	store := &slicestore.SliceStore{}
	store.Init()
	remote := khatru.NewRelay()
	remote.UseEventstore(store, 100)
	srv := httptest.NewServer(remote)
	defer srv.Close()
	remoteURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	relayKey := nostr.Generate()
	admin := nostr.Generate()
	var mu sync.Mutex
	var local []nostr.Event
	n := NewNotifier(relayKey, []nostr.PubKey{admin.Public()}, []string{remoteURL}, "wss://relay.example.com",
		func(_ context.Context, evt nostr.Event) error {
			mu.Lock()
			defer mu.Unlock()
			local = append(local, evt)
			return nil
		}, discard)

	n.Notify(context.Background(), "payments are down")

	mu.Lock()
	if len(local) != 1 {
		mu.Unlock()
		t.Fatalf("expected local delivery, got %d", len(local))
	}
	mu.Unlock()

	var wraps []nostr.Event
	for evt := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindGiftWrap}, Tags: nostr.TagMap{"p": {admin.Public().Hex()}}}, 10) {
		wraps = append(wraps, evt)
	}
	if len(wraps) != 1 {
		t.Fatalf("expected the gift wrap on the recipient's relay, got %d", len(wraps))
	}

	signer := keyer.NewPlainKeySigner(admin)
	rumor, err := nip59.GiftUnwrap(wraps[0], func(other nostr.PubKey, ct string) (string, error) {
		return signer.Decrypt(context.Background(), ct, other)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rumor.Content != "payments are down" || rumor.PubKey != relayKey.Public() || rumor.Kind != nostr.KindDirectMessage {
		t.Fatalf("unexpected rumor: %+v", rumor)
	}
}
