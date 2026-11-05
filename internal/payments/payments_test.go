package payments

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/pluja/nerostr/internal/store"
	"github.com/pluja/nerostr/internal/wallet"
	"github.com/pluja/nerostr/internal/xmr"
)

type fakeWallet struct {
	mu        sync.Mutex
	height    uint64
	next      uint32
	transfers []wallet.Transfer
	down      bool
	lastQuery []uint32
	lastMin   uint64
}

func (f *fakeWallet) Height(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return 0, errors.New("connection refused")
	}
	return f.height, nil
}

func (f *fakeWallet) CreateAddress(_ context.Context, _ string) (string, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", 0, errors.New("connection refused")
	}
	f.next++
	return fmt.Sprintf("8sub%d", f.next), f.next, nil
}

func (f *fakeWallet) IncomingTransfers(_ context.Context, idx []uint32, minHeight uint64) ([]wallet.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQuery, f.lastMin = idx, minHeight
	var out []wallet.Transfer
	for _, t := range f.transfers {
		for _, i := range idx {
			if t.SubaddrIndex.Minor == i && (t.Height == 0 || t.Height > minHeight) {
				out = append(out, t)
			}
		}
	}
	return out, nil
}

func (f *fakeWallet) pay(minor uint32, txid string, amount xmr.Piconero, height, confs uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := wallet.Transfer{TxID: txid, Amount: uint64(amount), Height: height, Confirmations: confs, Type: "in"}
	if height == 0 {
		t.Type = "pool"
	}
	t.SubaddrIndex.Minor = minor
	f.transfers = append(f.transfers, t)
}

type harness struct {
	svc   *Service
	st    *store.Store
	w     *fakeWallet
	clock time.Time
}

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := Config{
		Amount:            2_000_000_000,
		InvoiceTTL:        time.Hour,
		LatePaymentWindow: 30 * 24 * time.Hour,
		MinConfirmations:  1,
		MaxOpenInvoices:   10,
		PollInterval:      20 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h := &harness{st: st, w: &fakeWallet{height: 1000}, clock: time.Unix(1_800_000_000, 0)}
	h.svc = New(cfg, st, h.w, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.svc.now = func() time.Time { return h.clock }
	return h
}

func (h *harness) check(t *testing.T) {
	t.Helper()
	if _, _, err := h.svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) member(t *testing.T, pk nostr.PubKey) bool {
	t.Helper()
	ok, err := h.st.IsMember(context.Background(), pk)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestInvoiceIsReusedUntilExpiry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()

	a, err := h.svc.Invoice(ctx, pk)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.svc.Invoice(ctx, pk)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.Address != b.Address {
		t.Fatal("open invoice should be reused")
	}
	if a.WalletHeight != 1000 || a.Amount != 2_000_000_000 || !a.ExpiresAt.Equal(h.clock.Add(time.Hour)) {
		t.Fatalf("unexpected invoice: %+v", a)
	}

	h.clock = h.clock.Add(2 * time.Hour)
	c, err := h.svc.Invoice(ctx, pk)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == a.ID || c.SubaddrIndex == a.SubaddrIndex {
		t.Fatal("expired invoice must be replaced by a fresh subaddress")
	}
}

func TestFullPaymentNeedsConfirmation(t *testing.T) {
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)

	h.w.pay(inv.SubaddrIndex, "tx1", 2_000_000_000, 0, 0)
	h.check(t)
	if h.member(t, pk) {
		t.Fatal("mempool payment must not grant access with MIN_CONFIRMATIONS=1")
	}
	got, _ := h.st.LatestInvoice(context.Background(), pk)
	if got.Seen != 2_000_000_000 || got.Confirmed != 0 {
		t.Fatalf("totals = %+v", got)
	}

	h.w.transfers[0].Height, h.w.transfers[0].Confirmations = 1001, 1
	h.check(t)
	if !h.member(t, pk) {
		t.Fatal("confirmed payment must grant access")
	}
}

func TestZeroConfirmations(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MinConfirmations = 0 })
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)
	h.w.pay(inv.SubaddrIndex, "tx1", 2_000_000_000, 0, 0)
	h.check(t)
	if !h.member(t, pk) {
		t.Fatal("mempool payment must grant access with MIN_CONFIRMATIONS=0")
	}
}

func TestPartialPaymentsAddUp(t *testing.T) {
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)

	h.w.pay(inv.SubaddrIndex, "tx1", 1_500_000_000, 1001, 3)
	h.check(t)
	if h.member(t, pk) {
		t.Fatal("underpayment must not grant access")
	}
	h.w.pay(inv.SubaddrIndex, "tx2", 500_000_000, 1003, 1)
	h.check(t)
	if !h.member(t, pk) {
		t.Fatal("top-up must complete the payment")
	}
}

func TestDuplicateRowsAndDoubleSpends(t *testing.T) {
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)

	h.w.pay(inv.SubaddrIndex, "tx1", 1_000_000_000, 1001, 2)
	h.w.pay(inv.SubaddrIndex, "tx1", 1_000_000_000, 1001, 2)
	h.w.pay(inv.SubaddrIndex, "tx2", 1_000_000_000, 1001, 2)
	h.w.transfers[2].DoubleSpendSeen = true
	h.check(t)
	if h.member(t, pk) {
		t.Fatal("duplicated or double-spent transfers must not be counted")
	}
}

func TestLatePaymentIsCredited(t *testing.T) {
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)

	h.clock = h.clock.Add(5 * 24 * time.Hour)
	h.w.pay(inv.SubaddrIndex, "late", 2_000_000_000, 4000, 5)
	h.check(t)
	if !h.member(t, pk) {
		t.Fatal("payment to an expired invoice inside the late window must be credited")
	}
}

func TestPaymentAfterLateWindowIsIgnored(t *testing.T) {
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(context.Background(), pk)

	h.clock = h.clock.Add(31 * 24 * time.Hour)
	h.w.pay(inv.SubaddrIndex, "very-late", 2_000_000_000, 30000, 5)
	h.check(t)
	if h.member(t, pk) {
		t.Fatal("payments after the late window are not watched")
	}
}

func TestQueriesOnlyWatchedSubaddresses(t *testing.T) {
	h := newHarness(t, nil)
	a, _ := h.svc.Invoice(context.Background(), nostr.Generate().Public())
	h.w.height = 1500
	b, _ := h.svc.Invoice(context.Background(), nostr.Generate().Public())
	h.check(t)
	if len(h.w.lastQuery) != 2 || h.w.lastQuery[0] != a.SubaddrIndex || h.w.lastQuery[1] != b.SubaddrIndex {
		t.Fatalf("queried %v", h.w.lastQuery)
	}
	if h.w.lastMin != 1000-heightMargin {
		t.Fatalf("min height = %d", h.w.lastMin)
	}
}

func TestMembersAndBannedCannotGetInvoices(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	member := nostr.Generate().Public()
	banned := nostr.Generate().Public()
	h.st.AddMember(ctx, member, store.SourceAdmin, "")
	h.st.BanPubKey(ctx, banned, "")

	if _, err := h.svc.Invoice(ctx, member); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("member: %v", err)
	}
	if _, err := h.svc.Invoice(ctx, banned); !errors.Is(err, ErrBanned) {
		t.Fatalf("banned: %v", err)
	}
}

func TestOpenInvoiceCap(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxOpenInvoices = 2 })
	ctx := context.Background()
	for range 2 {
		if _, err := h.svc.Invoice(ctx, nostr.Generate().Public()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Invoice(ctx, nostr.Generate().Public()); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	h.clock = h.clock.Add(2 * time.Hour)
	if _, err := h.svc.Invoice(ctx, nostr.Generate().Public()); err != nil {
		t.Fatalf("cap should free up after expiry: %v", err)
	}
}

func TestWalletDown(t *testing.T) {
	h := newHarness(t, nil)
	h.w.down = true
	if _, err := h.svc.Invoice(context.Background(), nostr.Generate().Public()); !errors.Is(err, ErrWallet) {
		t.Fatalf("expected ErrWallet, got %v", err)
	}
	h.svc.checkAndRecord(context.Background())
	if h.svc.Healthy() {
		t.Fatal("must be unhealthy while the wallet is down")
	}
	h.w.down = false
	h.svc.checkAndRecord(context.Background())
	if !h.svc.Healthy() {
		t.Fatalf("must recover: %+v", h.svc.Status())
	}
}

func TestPartiallyPaidInvoiceIsKeptAfterExpiry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	pk := nostr.Generate().Public()
	inv, _ := h.svc.Invoice(ctx, pk)

	h.w.pay(inv.SubaddrIndex, "tx1", 1_000_000_000, 1001, 2)
	h.check(t)
	h.clock = h.clock.Add(2 * time.Hour)

	again, err := h.svc.Invoice(ctx, pk)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != inv.ID {
		t.Fatal("a partially paid invoice must be reused so the top-up lands on the same subaddress")
	}

	h.clock = h.clock.Add(31 * 24 * time.Hour)
	fresh, err := h.svc.Invoice(ctx, pk)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == inv.ID {
		t.Fatal("outside the late window a new invoice must be issued")
	}
}
