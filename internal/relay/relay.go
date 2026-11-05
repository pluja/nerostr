package relay

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/khatru/policies"
	"fiatjaf.com/nostr/nip11"
	"fiatjaf.com/nostr/nip86"

	"github.com/pluja/nerostr/internal/config"
	"github.com/pluja/nerostr/internal/pubkey"
	"github.com/pluja/nerostr/internal/store"
)

const (
	MaxMessageSize  = 256 << 10
	MaxQueryLimit   = 500
	MaxFutureDrift  = 15 * time.Minute
	SoftwareURL     = "https://github.com/pluja/nerostr"
	maxBanDeletions = 100_000
)

var Version = "dev"

type Relay struct {
	*khatru.Relay
	cfg        *config.Config
	store      *store.Store
	events     eventstore.Store
	log        *slog.Logger
	members    *memberCache
	inboxLimit func(context.Context, nostr.Event) (bool, string)
}

func New(cfg *config.Config, st *store.Store, events eventstore.Store, log *slog.Logger) *Relay {
	r := &Relay{
		Relay:      khatru.NewRelay(),
		cfg:        cfg,
		store:      st,
		events:     events,
		log:        log,
		members:    newMemberCache(st.IsMember),
		inboxLimit: policies.EventIPRateLimiter(10, time.Minute, 60),
	}
	rl := r.Relay

	rl.ServiceURL = cfg.PublicURL.String()
	rl.Log = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	rl.MaxMessageSize = MaxMessageSize
	rl.Negentropy = true
	rl.Info = r.info()

	rl.UseEventstore(events, MaxQueryLimit)
	rl.QueryStored = r.membersOnly(rl.QueryStored)
	rl.PreventBroadcast = r.preventFeedBroadcast

	connLimit := policies.ConnectionRateLimiter(10, time.Minute, 120)
	rl.RejectConnection = func(req *http.Request) bool {
		ip := khatru.GetIPFromRequest(req)
		if blocked, err := st.IsBlockedIP(req.Context(), ip); err != nil || blocked {
			return true
		}
		return connLimit(req)
	}

	rl.OnEvent = policies.SeqEvent(
		r.rejectBanned,
		policies.PreventTimestampsInTheFuture(MaxFutureDrift),
		r.requireMembership,
		policies.EventPubKeyRateLimiter(30, time.Minute, 300),
	)
	rl.OnRequest = policies.SeqRequest(
		policies.FilterIPRateLimiter(60, time.Minute, 300),
	)

	r.setupManagementAPI()
	return r
}

func (r *Relay) info() *nip11.RelayInformationDocument {
	cfg := r.cfg
	info := &nip11.RelayInformationDocument{
		Name:          cfg.RelayName,
		Description:   cfg.RelayDescription,
		PubKey:        cfg.RelayOperator,
		Contact:       cfg.RelayContact,
		Icon:          cfg.RelayIcon,
		Software:      SoftwareURL,
		Version:       Version,
		SupportedNIPs: []any{1, 11, 40, 42, 70, 86},
		PaymentsURL:   cfg.PublicURL.String(),
		Self:          selfPubKey(cfg),
		PostingPolicy: cfg.PublicURL.String(),
		Limitation: &nip11.RelayLimitationDocument{
			MaxMessageLength:    MaxMessageSize,
			MaxLimit:            MaxQueryLimit,
			DefaultLimit:        MaxQueryLimit,
			MaxSubidLength:      64,
			CreatedAtUpperLimit: int64(MaxFutureDrift.Seconds()),
			// Reads are free, so per NIP-11 payment_required stays false; writes are restricted to members.
			PaymentRequired:  false,
			RestrictedWrites: true,
		},
	}
	info.Fees = &nip11.RelayFeesDocument{}
	info.Fees.Admission = append(info.Fees.Admission, struct {
		Amount int
		Unit   string
	}{Amount: int(cfg.AdmissionAmount), Unit: "piconero"})
	return info
}

func selfPubKey(cfg *config.Config) *nostr.PubKey {
	if cfg.RelayKey == nil {
		return nil
	}
	pk := cfg.RelayKey.Public()
	return &pk
}

func (r *Relay) rejectBanned(ctx context.Context, evt nostr.Event) (bool, string) {
	if banned, err := r.store.IsBannedPubKey(ctx, evt.PubKey); err != nil {
		return true, "error: try again later"
	} else if banned {
		return true, "blocked: this key is banned from this relay"
	}
	if banned, err := r.store.IsBannedEvent(ctx, evt.ID); err != nil {
		return true, "error: try again later"
	} else if banned {
		return true, "blocked: this event is banned from this relay"
	}
	return false, ""
}

func (r *Relay) requireMembership(ctx context.Context, evt nostr.Event) (bool, string) {
	member, err := r.store.IsMember(ctx, evt.PubKey)
	if err != nil {
		r.log.Error("membership lookup failed", "err", err)
		return true, "error: try again later"
	}
	if member {
		return false, ""
	}
	// Former members and inbox senders may still delete what they published here.
	if evt.Kind == nostr.KindDeletion && r.hasEventsBy(evt.PubKey) {
		return false, ""
	}
	if r.cfg.Inbox && r.isInbox(ctx, evt) {
		return r.inboxLimit(ctx, evt)
	}
	return true, "restricted: pay the one-time admission fee at " + r.cfg.PublicURL.String() + " to publish here"
}

func (r *Relay) hasEventsBy(pk nostr.PubKey) bool {
	for range r.events.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{pk}, Limit: 1}, 1) {
		return true
	}
	return false
}

// deleteEventsBy removes everything a pubkey published here. IDs are collected
// first because deleting while an LMDB read cursor is open is not safe.
func (r *Relay) deleteEventsBy(pk nostr.PubKey) {
	var ids []nostr.ID
	for evt := range r.events.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{pk}, Limit: maxBanDeletions}, maxBanDeletions) {
		ids = append(ids, evt.ID)
	}
	deleted := 0
	for _, id := range ids {
		if err := r.events.DeleteEvent(id); err != nil {
			r.log.Error("deleting event of banned key", "id", id.Hex(), "err", err)
			continue
		}
		deleted++
	}
	r.log.Info("deleted events of banned key", "pubkey", pubkey.Short(pk), "count", deleted)
}

var errUnauthorized = errors.New("unauthorized: only relay admins can call the management API")

func (r *Relay) setupManagementAPI() {
	api := &r.Relay.ManagementAPI
	st := r.store

	api.OnAPICall = func(ctx context.Context, mp nip86.MethodParams) (bool, string) {
		caller, ok := khatru.GetAuthed(ctx)
		if ok && nostr.ContainsPubKey(r.cfg.AdminPubKeys, caller) {
			r.log.Info("management api call", "method", mp.MethodName(), "admin", pubkey.Short(caller))
			return false, ""
		}
		return true, errUnauthorized.Error()
	}

	api.AllowPubKey = func(ctx context.Context, pk nostr.PubKey, reason string) error {
		return st.AddMember(ctx, pk, store.SourceAdmin, reason)
	}
	api.UnallowPubKey = func(ctx context.Context, pk nostr.PubKey, _ string) error {
		return st.RemoveMember(ctx, pk)
	}
	api.ListAllowedPubKeys = func(ctx context.Context) ([]nip86.PubKeyReason, error) {
		members, err := st.ListMembers(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]nip86.PubKeyReason, len(members))
		for i, m := range members {
			reason := m.Source
			if m.Note != "" {
				reason += ": " + m.Note
			}
			out[i] = nip86.PubKeyReason{PubKey: m.PubKey, Reason: reason}
		}
		return out, nil
	}

	api.BanPubKey = func(ctx context.Context, pk nostr.PubKey, reason string) error {
		if err := st.BanPubKey(ctx, pk, reason); err != nil {
			return err
		}
		go r.deleteEventsBy(pk)
		return nil
	}
	api.UnbanPubKey = func(ctx context.Context, pk nostr.PubKey, _ string) error {
		return st.UnbanPubKey(ctx, pk)
	}
	api.ListBannedPubKeys = func(ctx context.Context) ([]nip86.PubKeyReason, error) {
		entries, err := st.ListBannedPubKeys(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]nip86.PubKeyReason, 0, len(entries))
		for _, e := range entries {
			if pk, err := nostr.PubKeyFromHexCheap(e.Key); err == nil {
				out = append(out, nip86.PubKeyReason{PubKey: pk, Reason: e.Reason})
			}
		}
		return out, nil
	}

	api.BanEvent = func(ctx context.Context, id nostr.ID, reason string) error {
		if err := st.BanEvent(ctx, id, reason); err != nil {
			return err
		}
		if err := r.events.DeleteEvent(id); err != nil {
			r.log.Warn("deleting banned event", "id", id.Hex(), "err", err)
		}
		return nil
	}
	api.AllowEvent = func(ctx context.Context, id nostr.ID, _ string) error {
		return st.UnbanEvent(ctx, id)
	}
	api.ListBannedEvents = func(ctx context.Context) ([]nip86.IDReason, error) {
		entries, err := st.ListBannedEvents(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]nip86.IDReason, 0, len(entries))
		for _, e := range entries {
			if id, err := nostr.IDFromHex(e.Key); err == nil {
				out = append(out, nip86.IDReason{ID: id, Reason: e.Reason})
			}
		}
		return out, nil
	}

	api.BlockIP = func(ctx context.Context, ip net.IP, reason string) error {
		return st.BlockIP(ctx, ip.String(), reason)
	}
	api.UnblockIP = func(ctx context.Context, ip net.IP, _ string) error {
		return st.UnblockIP(ctx, ip.String())
	}
	api.ListBlockedIPs = func(ctx context.Context) ([]nip86.IPReason, error) {
		entries, err := st.ListBlockedIPs(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]nip86.IPReason, len(entries))
		for i, e := range entries {
			out[i] = nip86.IPReason{IP: e.Key, Reason: e.Reason}
		}
		return out, nil
	}
}
