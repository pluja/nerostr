package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/pluja/nerostr/internal/pubkey"
	"github.com/pluja/nerostr/internal/store"
	"github.com/pluja/nerostr/internal/wallet"
	"github.com/pluja/nerostr/internal/xmr"
)

var (
	ErrAlreadyMember = errors.New("this key already has access")
	ErrBanned        = errors.New("this key is banned from this relay")
	ErrBusy          = errors.New("too many open invoices right now, please try again later")
	ErrWallet        = errors.New("the payment system is temporarily unavailable, please try again in a few minutes")
)

// heightMargin re-scans a few blocks below each invoice's creation height so
// that wallet lag or a short reorg can't hide a payment.
const heightMargin = 10

type Wallet interface {
	Height(ctx context.Context) (uint64, error)
	CreateAddress(ctx context.Context, label string) (string, uint32, error)
	IncomingTransfers(ctx context.Context, subaddrIndices []uint32, minHeight uint64) ([]wallet.Transfer, error)
}

type Config struct {
	Amount            xmr.Piconero
	InvoiceTTL        time.Duration
	LatePaymentWindow time.Duration
	MinConfirmations  uint64
	MaxOpenInvoices   int
	PollInterval      time.Duration
}

type Service struct {
	cfg    Config
	store  *store.Store
	wallet Wallet
	log    *slog.Logger
	now    func() time.Time

	createMu sync.Mutex
	kick     chan struct{}

	statusMu sync.RWMutex
	status   Status
}

type Status struct {
	LastCheck       time.Time
	LastError       string
	WalletHeight    uint64
	HeightChangedAt time.Time
	Watching        int
}

// WalletStallAfter is how long the wallet height may stay unchanged before the
// wallet is considered stuck. Monero targets a block every 2 minutes, and a
// wallet whose daemon is gone keeps answering RPC calls at a frozen height.
const WalletStallAfter = 30 * time.Minute

func New(cfg Config, st *store.Store, w Wallet, log *slog.Logger) *Service {
	return &Service{cfg: cfg, store: st, wallet: w, log: log, now: time.Now, kick: make(chan struct{}, 1)}
}

func (s *Service) Config() Config { return s.cfg }

func (s *Service) Status() Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

func (s *Service) Healthy() bool { return s.Problem() == "" }

// Problem describes why payments are not being processed, or "" if they are.
func (s *Service) Problem() string {
	st := s.Status()
	now := s.now()
	switch {
	case st.LastError != "":
		return "payment checks are failing: " + st.LastError
	case st.LastCheck.IsZero():
		return "no successful payment check yet"
	case now.Sub(st.LastCheck) >= 5*s.cfg.PollInterval:
		return fmt.Sprintf("no successful payment check since %s", st.LastCheck.UTC().Format(time.RFC3339))
	case now.Sub(st.HeightChangedAt) >= WalletStallAfter:
		return fmt.Sprintf("wallet stuck at height %d since %s; is its Monero daemon reachable and synced?",
			st.WalletHeight, st.HeightChangedAt.UTC().Format(time.RFC3339))
	}
	return ""
}

// Invoice returns the current open invoice for pk, creating one if needed.
func (s *Service) Invoice(ctx context.Context, pk nostr.PubKey) (store.Invoice, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	if banned, err := s.store.IsBannedPubKey(ctx, pk); err != nil {
		return store.Invoice{}, err
	} else if banned {
		return store.Invoice{}, ErrBanned
	}
	if member, err := s.store.IsMember(ctx, pk); err != nil {
		return store.Invoice{}, err
	} else if member {
		return store.Invoice{}, ErrAlreadyMember
	}

	now := s.now()
	latest, err := s.store.LatestInvoice(ctx, pk)
	// An invoice that already received money keeps being used after expiry so
	// that a top-up lands on the same subaddress instead of stranding the partial.
	watched := now.Sub(latest.CreatedAt) < s.cfg.LatePaymentWindow
	if err == nil && !latest.Paid() && (!latest.Expired(now) || (latest.Seen > 0 && watched)) {
		return latest, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Invoice{}, err
	}

	open, err := s.store.CountOpenInvoices(ctx, now)
	if err != nil {
		return store.Invoice{}, err
	}
	if open >= s.cfg.MaxOpenInvoices {
		return store.Invoice{}, ErrBusy
	}

	height, err := s.wallet.Height(ctx)
	if err != nil {
		s.log.Error("wallet unavailable while creating invoice", "err", err)
		return store.Invoice{}, ErrWallet
	}
	address, index, err := s.wallet.CreateAddress(ctx, "nerostr "+pubkey.Npub(pk))
	if err != nil {
		s.log.Error("could not create subaddress", "err", err)
		return store.Invoice{}, ErrWallet
	}
	inv := store.Invoice{
		PubKey:       pk,
		Address:      address,
		SubaddrIndex: index,
		Amount:       s.cfg.Amount,
		WalletHeight: height,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.cfg.InvoiceTTL),
	}
	if err := s.store.CreateInvoice(ctx, &inv); err != nil {
		return store.Invoice{}, fmt.Errorf("saving invoice: %w", err)
	}
	s.log.Info("invoice created", "pubkey", pubkey.Short(pk), "subaddress_index", index, "amount", inv.Amount.String())
	return inv, nil
}

// Kick asks the monitor to check payments now instead of waiting for the next tick.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run polls the wallet until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	var lastKick time.Time
	for {
		s.checkAndRecord(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kick:
			if wait := 5*time.Second - time.Since(lastKick); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			lastKick = time.Now()
		}
	}
}

func (s *Service) checkAndRecord(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	height, watching, err := s.Check(ctx)
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if err != nil {
		if s.status.LastError != err.Error() {
			s.log.Error("payment check failed", "err", err)
		}
		s.status.LastError = err.Error()
		return
	}
	if s.status.LastError != "" {
		s.log.Info("payment checks recovered")
	}
	changedAt := s.status.HeightChangedAt
	if height != s.status.WalletHeight || changedAt.IsZero() {
		changedAt = s.now()
	}
	s.status = Status{LastCheck: s.now(), WalletHeight: height, HeightChangedAt: changedAt, Watching: watching}
}

// Check credits payments for all watched invoices once.
func (s *Service) Check(ctx context.Context) (walletHeight uint64, watching int, err error) {
	walletHeight, err = s.wallet.Height(ctx)
	if err != nil {
		return 0, 0, err
	}
	invoices, err := s.store.WatchedInvoices(ctx, s.now().Add(-s.cfg.LatePaymentWindow))
	if err != nil {
		return walletHeight, 0, err
	}
	if len(invoices) == 0 {
		return walletHeight, 0, nil
	}

	indices := make([]uint32, len(invoices))
	minHeight := invoices[0].WalletHeight
	for i, inv := range invoices {
		indices[i] = inv.SubaddrIndex
		minHeight = min(minHeight, inv.WalletHeight)
	}
	if minHeight > heightMargin {
		minHeight -= heightMargin
	} else {
		minHeight = 0
	}

	transfers, err := s.wallet.IncomingTransfers(ctx, indices, minHeight)
	if err != nil {
		return walletHeight, len(invoices), err
	}

	type totals struct{ seen, confirmed xmr.Piconero }
	bySubaddr := make(map[uint32]*totals, len(invoices))
	counted := make(map[string]bool, len(transfers))
	for _, t := range transfers {
		if t.SubaddrIndex.Major != 0 || t.DoubleSpendSeen {
			continue
		}
		key := fmt.Sprintf("%s/%d", t.TxID, t.SubaddrIndex.Minor)
		if counted[key] {
			continue
		}
		counted[key] = true
		tot := bySubaddr[t.SubaddrIndex.Minor]
		if tot == nil {
			tot = &totals{}
			bySubaddr[t.SubaddrIndex.Minor] = tot
		}
		tot.seen += xmr.Piconero(t.Amount)
		if s.confirmed(t) {
			tot.confirmed += xmr.Piconero(t.Amount)
		}
	}

	now := s.now()
	for _, inv := range invoices {
		tot := bySubaddr[inv.SubaddrIndex]
		if tot == nil {
			continue
		}
		if tot.confirmed >= inv.Amount {
			if err := s.store.MarkPaid(ctx, inv.ID, tot.seen, tot.confirmed, now); err != nil {
				return walletHeight, len(invoices), fmt.Errorf("marking invoice %d paid: %w", inv.ID, err)
			}
			s.log.Info("invoice paid", "pubkey", pubkey.Short(inv.PubKey), "invoice", inv.ID,
				"received", tot.confirmed.String(), "late", inv.Expired(now))
			continue
		}
		if tot.seen != inv.Seen || tot.confirmed != inv.Confirmed {
			if err := s.store.UpdateInvoiceTotals(ctx, inv.ID, tot.seen, tot.confirmed); err != nil {
				return walletHeight, len(invoices), err
			}
			s.log.Info("payment detected", "pubkey", pubkey.Short(inv.PubKey), "invoice", inv.ID,
				"seen", tot.seen.String(), "confirmed", tot.confirmed.String(), "due", inv.Amount.String())
		}
	}
	return walletHeight, len(invoices), nil
}

func (s *Service) confirmed(t wallet.Transfer) bool {
	if s.cfg.MinConfirmations == 0 {
		return true
	}
	return t.Height > 0 && t.Confirmations >= s.cfg.MinConfirmations
}
