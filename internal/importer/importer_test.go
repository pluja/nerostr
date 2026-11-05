package importer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/nip19"
	"github.com/dgraph-io/badger/v4"

	"github.com/pluja/nerostr/internal/store"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func signed(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, content string, ts nostr.Timestamp) nostr.Event {
	t.Helper()
	evt := nostr.Event{Kind: kind, CreatedAt: ts, Content: content}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestImportEvents(t *testing.T) {
	events := &lmdb.LMDBBackend{Path: filepath.Join(t.TempDir(), "events"), MapSize: 1 << 30}
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	sk := nostr.Generate()
	note := signed(t, sk, 1, "hello", 1000)
	oldProfile := signed(t, sk, 0, `{"name":"old"}`, 1000)
	newProfile := signed(t, sk, 0, `{"name":"new"}`, 2000)
	ephemeral := signed(t, sk, 20001, "", 1000)
	tampered := signed(t, sk, 1, "original", 1000)
	tampered.Content = "tampered"

	var lines []string
	for _, e := range []nostr.Event{note, note, oldProfile, newProfile, ephemeral, tampered} {
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	lines = append(lines, "", "not json")

	st, err := ImportEvents(strings.NewReader(strings.Join(lines, "\n")), events, discard)
	if err != nil {
		t.Fatal(err)
	}
	want := EventStats{Imported: 3, Duplicates: 1, Invalid: 2, Skipped: 1}
	if st != want {
		t.Fatalf("stats = %+v, want %+v", st, want)
	}

	var profiles []nostr.Event
	for e := range events.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{0}}, 10) {
		profiles = append(profiles, e)
	}
	if len(profiles) != 1 || profiles[0].ID != newProfile.ID {
		t.Fatalf("replaceable events not resolved: %v", profiles)
	}
}

func TestMigrateV1(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nerostr-db")
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	paid := nostr.Generate().Public()
	manual := nostr.Generate().Public()
	pending := nostr.Generate().Public()
	put := func(pk nostr.PubKey, status int, tx string) {
		npub := nip19.EncodeNpub(pk)
		v, _ := json.Marshal(map[string]any{"pubkey": npub, "address": "8abc", "status": status, "amount": 0.002, "txhash": tx, "date_updated": 1})
		if err := db.Update(func(txn *badger.Txn) error { return txn.Set([]byte(npub), v) }); err != nil {
			t.Fatal(err)
		}
	}
	put(paid, 2, "deadbeef")
	put(manual, 2, "manually_added")
	put(pending, 0, "")
	db.Update(func(txn *badger.Txn) error { return txn.Set([]byte("junk"), []byte("{")) })
	db.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	stats, err := MigrateV1(ctx, dir, t.TempDir(), st, discard)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (MemberStats{Imported: 2, Pending: 1, Invalid: 1}) {
		t.Fatalf("stats = %+v", stats)
	}
	for _, pk := range []nostr.PubKey{paid, manual} {
		m, err := st.GetMember(ctx, pk)
		if err != nil || m.Source != store.SourceImport {
			t.Fatalf("member %s: %+v %v", pk.Hex(), m, err)
		}
	}
	if ok, _ := st.IsMember(ctx, pending); ok {
		t.Fatal("unpaid v1 user must not become a member")
	}

	// Running it twice is harmless.
	if _, err := MigrateV1(ctx, dir, t.TempDir(), st, discard); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountMembers(ctx); n != 2 {
		t.Fatalf("members after rerun = %d", n)
	}
}
