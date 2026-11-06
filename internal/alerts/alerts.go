package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip17"

	"github.com/pluja/nerostr/internal/pubkey"
)

// Notifier sends NIP-17 direct messages signed by the relay's own key.
type Notifier struct {
	signer       keyer.KeySigner
	recipients   []nostr.PubKey
	lookupRelays []string
	ownRelay     string
	deliverLocal func(context.Context, nostr.Event) error
	pool         *nostr.Pool
	log          *slog.Logger

	mu       sync.Mutex
	dmRelays map[nostr.PubKey]cachedRelays
}

type cachedRelays struct {
	urls []string
	at   time.Time
}

// NewNotifier delivers every message to this relay through deliverLocal and to
// each recipient's NIP-17 DM relays, falling back to lookupRelays.
func NewNotifier(key nostr.SecretKey, recipients []nostr.PubKey, lookupRelays []string, ownRelay string,
	deliverLocal func(context.Context, nostr.Event) error, log *slog.Logger) *Notifier {
	return &Notifier{
		signer:       keyer.NewPlainKeySigner(key),
		recipients:   recipients,
		lookupRelays: lookupRelays,
		ownRelay:     nostr.NormalizeURL(ownRelay),
		deliverLocal: deliverLocal,
		pool:         nostr.NewPool(),
		log:          log,
		dmRelays:     map[nostr.PubKey]cachedRelays{},
	}
}

// SendTo DMs one key, e.g. a new member, through the same delivery as alerts.
func (n *Notifier) SendTo(ctx context.Context, to nostr.PubKey, text string) error {
	return n.send(ctx, to, text)
}

func (n *Notifier) Notify(ctx context.Context, text string) {
	for _, pk := range n.recipients {
		if err := n.send(ctx, pk, text); err != nil {
			n.log.Error("alert not delivered", "to", pubkey.Short(pk), "err", err)
		}
	}
}

func (n *Notifier) send(ctx context.Context, to nostr.PubKey, text string) error {
	_, wrap, err := nip17.PrepareMessage(ctx, text, nil, n.signer, to, nil)
	if err != nil {
		return err
	}
	delivered := 0
	if err := n.deliverLocal(ctx, wrap); err != nil {
		n.log.Warn("storing alert locally", "err", err)
	} else {
		delivered++
	}
	for _, url := range n.relaysFor(ctx, to) {
		if nostr.NormalizeURL(url) == n.ownRelay {
			continue
		}
		pubCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := n.pool.EnsureRelay(url)
		if err == nil {
			err = r.Publish(pubCtx, wrap)
		}
		cancel()
		if err != nil {
			n.log.Debug("alert relay refused", "relay", url, "err", err)
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return fmt.Errorf("no relay accepted the message")
	}
	n.log.Info("alert sent", "to", pubkey.Short(to), "relays", delivered)
	return nil
}

func (n *Notifier) relaysFor(ctx context.Context, pk nostr.PubKey) []string {
	n.mu.Lock()
	c, ok := n.dmRelays[pk]
	n.mu.Unlock()
	if ok && time.Since(c.at) < 6*time.Hour {
		return c.urls
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	urls := nip17.GetDMRelays(lookupCtx, pk, n.pool, n.lookupRelays)
	if len(urls) == 0 {
		urls = slices.Clone(n.lookupRelays)
	}
	n.mu.Lock()
	n.dmRelays[pk] = cachedRelays{urls: urls, at: time.Now()}
	n.mu.Unlock()
	return urls
}

// Monitor turns a health probe into alerts and heartbeat pings.
type Monitor struct {
	Name         string
	Problem      func() string
	Notify       func(ctx context.Context, text string)
	After        time.Duration
	HeartbeatURL string
	Heartbeat    time.Duration
	Log          *slog.Logger

	now      func() time.Time
	since    time.Time
	alerted  bool
	lastBeat time.Time
	client   *http.Client
}

func (m *Monitor) Run(ctx context.Context) {
	if m.now == nil {
		m.now = time.Now
	}
	m.client = &http.Client{Timeout: 10 * time.Second}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	m.step(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.step(ctx)
		}
	}
}

func (m *Monitor) step(ctx context.Context) {
	now := m.now()
	problem := m.Problem()

	if problem != "" {
		if m.since.IsZero() {
			m.since = now
		}
		if !m.alerted && now.Sub(m.since) >= m.After {
			m.alerted = true
			m.Log.Warn("sending alert", "problem", problem)
			if m.Notify != nil {
				m.Notify(ctx, fmt.Sprintf("⚠️ %s: payments are not being processed.\n\n%s\n\nSince %s.",
					m.Name, problem, m.since.UTC().Format("2006-01-02 15:04 UTC")))
			}
		}
		return
	}

	if m.alerted && m.Notify != nil {
		m.Notify(ctx, fmt.Sprintf("✅ %s: payments are working again (after %s).",
			m.Name, now.Sub(m.since).Round(time.Minute)))
	}
	m.since, m.alerted = time.Time{}, false

	if m.HeartbeatURL != "" && now.Sub(m.lastBeat) >= m.Heartbeat {
		m.lastBeat = now
		m.beat(ctx)
	}
}

func (m *Monitor) beat(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.HeartbeatURL, nil)
	if err != nil {
		return
	}
	resp, err := m.client.Do(req)
	if err != nil {
		m.Log.Warn("heartbeat failed", "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		m.Log.Warn("heartbeat rejected", "status", resp.Status)
	}
}
