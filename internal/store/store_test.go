package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "nerostr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReopenKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nerostr.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pk := nostr.Generate().Public()
	if err := s.AddMember(context.Background(), pk, SourceAdmin, ""); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, _ := s.IsMember(context.Background(), pk); !ok {
		t.Fatal("member lost after reopen")
	}
}

func TestMembersAndBans(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	pk := nostr.Generate().Public()

	if ok, err := s.IsMember(ctx, pk); err != nil || ok {
		t.Fatalf("IsMember = %v, %v", ok, err)
	}
	for range 2 {
		if err := s.AddMember(ctx, pk, SourceAdmin, "first"); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.CountMembers(ctx); n != 1 {
		t.Fatalf("CountMembers = %d", n)
	}
	m, err := s.GetMember(ctx, pk)
	if err != nil || m.Source != SourceAdmin || m.Note != "first" {
		t.Fatalf("GetMember = %+v, %v", m, err)
	}

	if err := s.BanPubKey(ctx, pk, "spam"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsMember(ctx, pk); ok {
		t.Fatal("ban must revoke membership")
	}
	if ok, _ := s.IsBannedPubKey(ctx, pk); !ok {
		t.Fatal("expected banned")
	}
	bans, _ := s.ListBannedPubKeys(ctx)
	if len(bans) != 1 || bans[0].Key != pk.Hex() || bans[0].Reason != "spam" {
		t.Fatalf("ListBannedPubKeys = %+v", bans)
	}

	if err := s.AddMember(ctx, pk, SourceAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsBannedPubKey(ctx, pk); ok {
		t.Fatal("explicit allow must lift the ban")
	}

	if err := s.RemoveMember(ctx, pk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMember(ctx, pk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMember after remove: %v", err)
	}
}

func TestEventAndIPBans(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	id := nostr.ID{1, 2, 3}
	if err := s.BanEvent(ctx, id, "illegal"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsBannedEvent(ctx, id); !ok {
		t.Fatal("event not banned")
	}
	if err := s.UnbanEvent(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsBannedEvent(ctx, id); ok {
		t.Fatal("event still banned")
	}

	if err := s.BlockIP(ctx, "203.0.113.7", "abuse"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsBlockedIP(ctx, "203.0.113.7"); !ok {
		t.Fatal("ip not blocked")
	}
	ips, _ := s.ListBlockedIPs(ctx)
	if len(ips) != 1 {
		t.Fatalf("ListBlockedIPs = %+v", ips)
	}
}

func TestInvoiceLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	pk := nostr.Generate().Public()
	t0 := time.Unix(1_700_000_000, 0)

	if _, err := s.LatestInvoice(ctx, pk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LatestInvoice on empty: %v", err)
	}

	inv := &Invoice{PubKey: pk, Address: "addr1", SubaddrIndex: 1, Amount: 2_000_000_000,
		WalletHeight: 100, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := s.CreateInvoice(ctx, inv); err != nil {
		t.Fatal(err)
	}
	dup := *inv
	dup.ID = 0
	if err := s.CreateInvoice(ctx, &dup); err == nil {
		t.Fatal("subaddress reuse must be rejected")
	}

	if n, _ := s.CountOpenInvoices(ctx, t0.Add(time.Minute)); n != 1 {
		t.Fatalf("CountOpenInvoices = %d", n)
	}
	if n, _ := s.CountOpenInvoices(ctx, t0.Add(2*time.Hour)); n != 0 {
		t.Fatalf("CountOpenInvoices after expiry = %d", n)
	}

	watched, _ := s.WatchedInvoices(ctx, t0.Add(-time.Hour))
	if len(watched) != 1 || watched[0].Address != "addr1" {
		t.Fatalf("WatchedInvoices = %+v", watched)
	}
	if !watched[0].Expired(t0.Add(2 * time.Hour)) {
		t.Fatal("expected expired")
	}

	if err := s.UpdateInvoiceTotals(ctx, inv.ID, 1_000_000_000, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestInvoice(ctx, pk)
	if got.Seen != 1_000_000_000 || got.Confirmed != 0 || got.Paid() {
		t.Fatalf("after partial: %+v", got)
	}

	paidAt := t0.Add(3 * time.Hour)
	if joined, err := s.MarkPaid(ctx, inv.ID, 2_000_000_000, 2_000_000_000, paidAt); err != nil || !joined {
		t.Fatalf("first MarkPaid: joined=%v err=%v", joined, err)
	}
	if joined, err := s.MarkPaid(ctx, inv.ID, 3_000_000_000, 3_000_000_000, paidAt.Add(time.Hour)); err != nil || joined {
		t.Fatalf("second MarkPaid must be a no-op: joined=%v err=%v", joined, err)
	}
	got, _ = s.LatestInvoice(ctx, pk)
	if !got.Paid() || !got.PaidAt.Equal(paidAt) || got.Confirmed != 2_000_000_000 {
		t.Fatalf("after paid: %+v", got)
	}
	m, err := s.GetMember(ctx, pk)
	if err != nil || m.Source != SourcePayment {
		t.Fatalf("member after payment: %+v %v", m, err)
	}
	if watched, _ := s.WatchedInvoices(ctx, t0.Add(-time.Hour)); len(watched) != 0 {
		t.Fatalf("paid invoice still watched: %+v", watched)
	}
}

func TestPaymentDoesNotOverrideBan(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	pk := nostr.Generate().Public()
	inv := &Invoice{PubKey: pk, Address: "a", SubaddrIndex: 7, Amount: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateInvoice(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := s.BanPubKey(ctx, pk, "abuse"); err != nil {
		t.Fatal(err)
	}
	if joined, err := s.MarkPaid(ctx, inv.ID, 1, 1, time.Now()); err != nil || joined {
		t.Fatalf("a banned key must not join: joined=%v err=%v", joined, err)
	}
	if ok, _ := s.IsMember(ctx, pk); ok {
		t.Fatal("banned key became a member by paying")
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	pk := nostr.Generate().Public()
	s.AddMember(ctx, pk, SourceAdmin, "")
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if ok, _ := b.IsMember(ctx, pk); !ok {
		t.Fatal("backup missing member")
	}
}

func TestMarkPaidForExistingMember(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	pk := nostr.Generate().Public()
	s.AddMember(ctx, pk, SourceAdmin, "")
	inv := &Invoice{PubKey: pk, Address: "a", SubaddrIndex: 9, Amount: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateInvoice(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if joined, err := s.MarkPaid(ctx, inv.ID, 1, 1, time.Now()); err != nil || joined {
		t.Fatalf("a key that was already a member did not join now: joined=%v err=%v", joined, err)
	}
}
