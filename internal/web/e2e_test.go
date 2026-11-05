package web_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/nip19"

	"github.com/pluja/nerostr/internal/config"
	"github.com/pluja/nerostr/internal/payments"
	"github.com/pluja/nerostr/internal/relay"
	"github.com/pluja/nerostr/internal/store"
	"github.com/pluja/nerostr/internal/wallet"
	"github.com/pluja/nerostr/internal/web"
	"github.com/pluja/nerostr/internal/xmr"
)

type fakeWallet struct {
	mu        sync.Mutex
	next      uint32
	transfers []wallet.Transfer
}

func (f *fakeWallet) Height(context.Context) (uint64, error) { return 3000, nil }

func (f *fakeWallet) CreateAddress(context.Context, string) (string, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return fmt.Sprintf("8FakeSubaddress%04d", f.next), f.next, nil
}

func (f *fakeWallet) IncomingTransfers(_ context.Context, idx []uint32, _ uint64) ([]wallet.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wallet.Transfer(nil), f.transfers...), nil
}

func (f *fakeWallet) pay(minor uint32, amount xmr.Piconero) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := wallet.Transfer{TxID: fmt.Sprintf("tx%d", len(f.transfers)), Amount: uint64(amount), Height: 3001, Confirmations: 3}
	t.SubaddrIndex.Minor = minor
	f.transfers = append(f.transfers, t)
}

type env struct {
	base   string
	cfg    *config.Config
	store  *store.Store
	events *lmdb.LMDBBackend
	pay    *payments.Service
	wallet *fakeWallet
	admin  nostr.SecretKey
	http   *http.Client
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	publicURL, _ := url.Parse(base)
	admin := nostr.Generate()

	cfg := &config.Config{
		PublicURL:         publicURL,
		RelayName:         "test relay",
		AdminPubKeys:      []nostr.PubKey{admin.Public()},
		AdmissionAmount:   2_000_000_000,
		InvoiceTTL:        time.Hour,
		LatePaymentWindow: 24 * time.Hour,
		MinConfirmations:  1,
		MaxOpenInvoices:   100,
		PollInterval:      time.Hour,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		LegacyAPIKey:      "0123456789abcdef0123",
	}
	st, err := store.Open(filepath.Join(dir, "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	events := &lmdb.LMDBBackend{Path: filepath.Join(dir, "events"), MapSize: 1 << 30}
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := &fakeWallet{}
	pay := payments.New(payments.Config{
		Amount: cfg.AdmissionAmount, InvoiceTTL: cfg.InvoiceTTL, LatePaymentWindow: cfg.LatePaymentWindow,
		MinConfirmations: cfg.MinConfirmations, MaxOpenInvoices: cfg.MaxOpenInvoices, PollInterval: cfg.PollInterval,
	}, st, w, log)

	rl := relay.New(cfg, st, events, log)
	site, err := web.New(cfg, st, pay, log, "test")
	if err != nil {
		t.Fatal(err)
	}
	site.Register(rl.Router())
	srv := &http.Server{Handler: web.RealIP(cfg.TrustedProxies, web.SecurityHeaders(web.LimitBody(1<<20, rl)))}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		rl.DisconnectAll()
		events.Close()
		st.Close()
	})

	return &env{
		base: base, cfg: cfg, store: st, events: events, pay: pay, wallet: w, admin: admin,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (e *env) connect(t *testing.T) *nostr.Relay {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := nostr.RelayConnect(ctx, "ws"+strings.TrimPrefix(e.base, "http"), nostr.RelayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func note(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, content string, tags nostr.Tags) nostr.Event {
	t.Helper()
	evt := nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: content, Tags: tags}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return evt
}

func publish(t *testing.T, r *nostr.Relay, evt nostr.Event) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.Publish(ctx, evt)
}

func (e *env) query(t *testing.T, filter nostr.Filter) []nostr.Event {
	t.Helper()
	var out []nostr.Event
	for evt := range e.events.QueryEvents(filter, 100) {
		out = append(out, evt)
	}
	return out
}

func (e *env) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := e.http.Get(e.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) nip86(t *testing.T, sk nostr.SecretKey, method string, params ...any) (result any, errMsg string) {
	t.Helper()
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"method": method, "params": params})
	hash := sha256.Sum256(body)
	auth := note(t, sk, 27235, "", nostr.Tags{{"u", e.base}, {"method", "POST"}, {"payload", hex.EncodeToString(hash[:])}})
	authJSON, _ := json.Marshal(auth)

	req, _ := http.NewRequest(http.MethodPost, e.base+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/nostr+json+rpc")
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(authJSON))
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Result, out.Error
}

func TestPayToRelayFlow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := nostr.Generate()
	npub := nip19.EncodeNpub(user.Public())
	r := e.connect(t)

	// Non-members are told where to pay.
	err := publish(t, r, note(t, user, 1, "hello", nil))
	if err == nil || !strings.Contains(err.Error(), "restricted") || !strings.Contains(err.Error(), e.base) {
		t.Fatalf("expected paywall rejection, got %v", err)
	}

	// Request an invoice with the hex key; we land on the canonical npub page.
	resp, err := e.http.PostForm(e.base+"/user", url.Values{"pkey": {user.Public().Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/user/"+npub {
		t.Fatalf("POST /user = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, page := e.get(t, "/user/"+npub)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "8FakeSubaddress0001") || !strings.Contains(page, "0.002") {
		t.Fatalf("invoice page: %d\n%s", resp.StatusCode, page)
	}
	if !strings.Contains(page, `href="monero:8FakeSubaddress0001?tx_amount=0.002&amp;`) {
		t.Fatalf("wallet link missing or sanitized:\n%s", page)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("missing CSP")
	}
	if resp, _ := e.get(t, "/user/"+npub+"/qr.png"); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("qr: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Same key again reuses the invoice.
	resp, _ = e.http.PostForm(e.base+"/user", url.Values{"pkey": {npub}})
	resp.Body.Close()
	inv, err := e.store.LatestInvoice(ctx, user.Public())
	if err != nil || inv.SubaddrIndex != 1 {
		t.Fatalf("invoice not reused: %+v %v", inv, err)
	}

	// Partial payment shows up in the status API.
	e.wallet.pay(1, 1_000_000_000)
	if _, _, err := e.pay.Check(ctx); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/api/user/"+npub)
	var st struct {
		Member  bool
		Invoice struct{ Seen, Remaining string }
	}
	json.Unmarshal([]byte(body), &st)
	if st.Member || st.Invoice.Seen != "0.001" || st.Invoice.Remaining != "0.001" {
		t.Fatalf("status after partial payment: %s", body)
	}

	// Completing the payment grants access.
	e.wallet.pay(1, 1_000_000_000)
	if _, _, err := e.pay.Check(ctx); err != nil {
		t.Fatal(err)
	}
	_, page = e.get(t, "/user/"+npub)
	if !strings.Contains(page, "You're in") {
		t.Fatalf("member page:\n%s", page)
	}
	if _, body := e.get(t, "/api/status/"+npub); !strings.Contains(body, `"accept"`) {
		t.Fatalf("legacy status: %s", body)
	}

	evt := note(t, user, 1, "hello paid relay", nil)
	if err := publish(t, r, evt); err != nil {
		t.Fatalf("member publish: %v", err)
	}
	if got := e.query(t, nostr.Filter{IDs: []nostr.ID{evt.ID}}); len(got) != 1 {
		t.Fatalf("event not stored: %v", got)
	}

	// Reading is free for anyone.
	reader := e.connect(t)
	var found bool
	for range reader.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{user.Public()}}) {
		found = true
	}
	if !found {
		t.Fatal("reader could not fetch the member's note")
	}

	// Index page counts the member.
	if _, page := e.get(t, "/"); !strings.Contains(page, "1 member can publish") {
		t.Fatalf("index:\n%s", page)
	}
}

func TestManagementAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := nostr.Generate()
	stranger := nostr.Generate()
	r := e.connect(t)

	if _, errMsg := e.nip86(t, stranger, "allowpubkey", user.Public().Hex(), "nope"); !strings.Contains(errMsg, "unauthorized") {
		t.Fatalf("non-admin call: %q", errMsg)
	}
	if ok, _ := e.store.IsMember(ctx, user.Public()); ok {
		t.Fatal("non-admin managed to add a member")
	}

	if res, errMsg := e.nip86(t, e.admin, "supportedmethods"); errMsg != "" || !strings.Contains(fmt.Sprint(res), "banpubkey") {
		t.Fatalf("supportedmethods: %v %q", res, errMsg)
	}
	if _, errMsg := e.nip86(t, e.admin, "allowpubkey", user.Public().Hex(), "friend"); errMsg != "" {
		t.Fatalf("allowpubkey: %q", errMsg)
	}
	res, _ := e.nip86(t, e.admin, "listallowedpubkeys")
	if !strings.Contains(fmt.Sprint(res), user.Public().Hex()) {
		t.Fatalf("listallowedpubkeys: %v", res)
	}

	first := note(t, user, 1, "first", nil)
	second := note(t, user, 1, "second", nil)
	for _, evt := range []nostr.Event{first, second} {
		if err := publish(t, r, evt); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	// Banning a single event deletes it and blocks re-upload.
	if _, errMsg := e.nip86(t, e.admin, "banevent", first.ID.Hex(), "rude"); errMsg != "" {
		t.Fatalf("banevent: %q", errMsg)
	}
	if got := e.query(t, nostr.Filter{IDs: []nostr.ID{first.ID}}); len(got) != 0 {
		t.Fatal("banned event still stored")
	}
	if err := publish(t, r, first); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("re-upload of banned event: %v", err)
	}

	// Banning a key blocks it and removes everything it published.
	if _, errMsg := e.nip86(t, e.admin, "banpubkey", user.Public().Hex(), "spam"); errMsg != "" {
		t.Fatalf("banpubkey: %q", errMsg)
	}
	if err := publish(t, r, note(t, user, 1, "still here?", nil)); err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("banned key publish: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.query(t, nostr.Filter{Authors: []nostr.PubKey{user.Public()}})) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("events of banned key were not deleted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, errMsg := e.nip86(t, e.admin, "blockip", "203.0.113.9", "abuse"); errMsg != "" {
		t.Fatalf("blockip: %q", errMsg)
	}
	if ok, _ := e.store.IsBlockedIP(ctx, "203.0.113.9"); !ok {
		t.Fatal("ip not blocked")
	}
}

func TestFormerMemberCanDelete(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := nostr.Generate()
	r := e.connect(t)

	e.store.AddMember(ctx, user.Public(), store.SourceAdmin, "")
	evt := note(t, user, 1, "regret", nil)
	if err := publish(t, r, evt); err != nil {
		t.Fatal(err)
	}
	e.store.RemoveMember(ctx, user.Public())

	if err := publish(t, r, note(t, user, 1, "new post", nil)); err == nil {
		t.Fatal("former member must not publish new notes")
	}
	del := note(t, user, nostr.KindDeletion, "", nostr.Tags{{"e", evt.ID.Hex()}})
	if err := publish(t, r, del); err != nil {
		t.Fatalf("deletion by former member: %v", err)
	}
	if got := e.query(t, nostr.Filter{IDs: []nostr.ID{evt.ID}}); len(got) != 0 {
		t.Fatal("event not deleted")
	}

	stranger := nostr.Generate()
	if err := publish(t, r, note(t, stranger, nostr.KindDeletion, "", nostr.Tags{{"e", evt.ID.Hex()}})); err == nil {
		t.Fatal("strangers without events here must not publish deletions")
	}
}

func TestLegacyAPI(t *testing.T) {
	e := setup(t)
	pk := nostr.Generate().Public()
	call := func(method, key string) int {
		req, _ := http.NewRequest(method, e.base+"/api/user/"+nip19.EncodeNpub(pk), nil)
		if key != "" {
			req.Header.Set("X-API-KEY", key)
		}
		resp, err := e.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := call(http.MethodPost, "wrong-key-wrong-key"); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	if code := call(http.MethodPost, ""); code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", code)
	}
	if code := call(http.MethodPost, e.cfg.LegacyAPIKey); code != 200 {
		t.Fatalf("add: %d", code)
	}
	if ok, _ := e.store.IsMember(context.Background(), pk); !ok {
		t.Fatal("not added")
	}
	if code := call(http.MethodDelete, e.cfg.LegacyAPIKey); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if ok, _ := e.store.IsMember(context.Background(), pk); ok {
		t.Fatal("not removed")
	}
}

func TestInvalidKeysAndHealth(t *testing.T) {
	e := setup(t)
	resp, _ := e.http.PostForm(e.base+"/user", url.Values{"pkey": {"npub1garbage"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/?error=invalid-key" {
		t.Fatalf("invalid key redirect: %s", resp.Header.Get("Location"))
	}
	if _, page := e.get(t, "/?error=invalid-key"); !strings.Contains(page, "doesn&#39;t look like") {
		t.Fatalf("error not rendered:\n%s", page)
	}
	if _, page := e.get(t, "/?error=<script>"); strings.Contains(page, "<script>") {
		t.Fatal("arbitrary error text must not be reflected")
	}
	if resp, body := e.get(t, "/healthz"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"database":"ok"`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}

	req, _ := http.NewRequest(http.MethodGet, e.base+"/", nil)
	req.Header.Set("Accept", "application/nostr+json")
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info map[string]any
	json.NewDecoder(resp.Body).Decode(&info)
	if info["payments_url"] != e.base || fmt.Sprint(info["fees"]) == "" {
		t.Fatalf("nip11: %v", info)
	}
}

func (e *env) remoteQuery(t *testing.T, r *nostr.Relay, filter nostr.Filter) []nostr.Event {
	t.Helper()
	var out []nostr.Event
	for evt := range r.QueryEvents(filter) {
		out = append(out, evt)
	}
	return out
}

func ids(events []nostr.Event) map[nostr.ID]bool {
	m := map[nostr.ID]bool{}
	for _, e := range events {
		m[e.ID] = true
	}
	return m
}

func TestInbox(t *testing.T) {
	e := setup(t)
	e.cfg.Inbox = true
	e.cfg.InboxKinds = []nostr.Kind{1, 7, 1059}
	ctx := context.Background()
	member := nostr.Generate()
	stranger := nostr.Generate()
	e.store.AddMember(ctx, member.Public(), store.SourceAdmin, "")
	r := e.connect(t)
	mp := member.Public().Hex()

	// Live subscriptions: a generic feed and the member's notifications.
	// Subscribing before anything is stored avoids a race in the client library.
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	feed, err := e.connect(t).Subscribe(subCtx, nostr.Filter{Kinds: []nostr.Kind{1}}, nostr.SubscriptionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	notifs, err := e.connect(t).Subscribe(subCtx, nostr.Filter{Tags: nostr.TagMap{"p": {mp}}}, nostr.SubscriptionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-feed.EndOfStoredEvents
	<-notifs.EndOfStoredEvents

	post := note(t, member, 1, "members-only global feed", nil)
	if err := publish(t, r, post); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-feed.Events:
		if got.ID != post.ID {
			t.Fatalf("feed got %q, want the member post", got.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generic feed did not receive the member's post")
	}

	reply := note(t, stranger, 1, "nice post", nostr.Tags{{"e", post.ID.Hex()}, {"p", mp}})
	if err := publish(t, r, reply); err != nil {
		t.Fatalf("reply to a member must be accepted: %v", err)
	}
	wrap := note(t, nostr.Generate(), 1059, "ciphertext", nostr.Tags{{"p", mp}})
	if err := publish(t, r, wrap); err != nil {
		t.Fatalf("gift-wrapped DM to a member must be accepted: %v", err)
	}

	live := map[nostr.ID]bool{}
	for len(live) < 2 {
		select {
		case got := <-notifs.Events:
			live[got.ID] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("member's notifications got %v, want the reply and the DM", live)
		}
	}
	if !live[reply.ID] || !live[wrap.ID] {
		t.Fatalf("member's notifications got %v, want the reply and the DM", live)
	}
	select {
	case got := <-feed.Events:
		t.Fatalf("generic feed received a non-member event: %q", got.Content)
	case <-time.After(500 * time.Millisecond):
	}

	rejected := map[string]nostr.Event{
		"no recipient":         note(t, stranger, 1, "spam", nil),
		"non-member recipient": note(t, stranger, 1, "spam", nostr.Tags{{"p", nostr.Generate().Public().Hex()}}),
		"kind not allowed":     note(t, stranger, 30023, "article", nostr.Tags{{"d", "x"}, {"p", mp}}),
	}
	mass := nostr.Tags{{"p", mp}}
	for range maxRecipientsForTest {
		mass = append(mass, nostr.Tag{"p", nostr.Generate().Public().Hex()})
	}
	rejected["too many recipients"] = note(t, stranger, 1, "mass mention", mass)
	for name, evt := range rejected {
		if err := publish(t, r, evt); err == nil || !strings.Contains(err.Error(), "restricted") {
			t.Errorf("%s: expected rejection, got %v", name, err)
		}
	}

	reader := e.connect(t)
	global := ids(e.remoteQuery(t, reader, nostr.Filter{Kinds: []nostr.Kind{1}}))
	if !global[post.ID] || global[reply.ID] {
		t.Fatalf("global feed must contain only member posts: %v", global)
	}
	thread := ids(e.remoteQuery(t, reader, nostr.Filter{Tags: nostr.TagMap{"e": {post.ID.Hex()}}}))
	if !thread[reply.ID] {
		t.Fatal("thread view must include replies from non-members")
	}
	dms := ids(e.remoteQuery(t, reader, nostr.Filter{Kinds: []nostr.Kind{1059}, Tags: nostr.TagMap{"p": {mp}}}))
	if !dms[wrap.ID] {
		t.Fatal("member must be able to fetch their DMs")
	}
	if byAuthor := ids(e.remoteQuery(t, reader, nostr.Filter{Authors: []nostr.PubKey{stranger.Public()}})); !byAuthor[reply.ID] {
		t.Fatal("asking for an author directly must return their events")
	}

	// Inbox senders can delete what they sent.
	del := note(t, stranger, nostr.KindDeletion, "", nostr.Tags{{"e", reply.ID.Hex()}})
	if err := publish(t, r, del); err != nil {
		t.Fatalf("inbox sender deleting own reply: %v", err)
	}

	e.cfg.Inbox = false
	if err := publish(t, r, note(t, stranger, 1, "another", nostr.Tags{{"p", mp}})); err == nil {
		t.Fatal("with INBOX=false non-members must be rejected")
	}
}

const maxRecipientsForTest = 20

func TestFloodGuard(t *testing.T) {
	e := setup(t)
	member := nostr.Generate()
	e.store.AddMember(context.Background(), member.Public(), store.SourceAdmin, "")
	r := e.connect(t)

	first := note(t, member, 1, "Issue 201 is out", nil)
	if err := publish(t, r, first); err != nil {
		t.Fatal(err)
	}
	if err := publish(t, r, first); err != nil {
		t.Fatalf("re-sending the same event must stay harmless: %v", err)
	}
	repeat := nostr.Event{Kind: 1, CreatedAt: first.CreatedAt + 90, Content: first.Content}
	if err := repeat.Sign(member); err != nil {
		t.Fatal(err)
	}
	if err := publish(t, r, repeat); err == nil || !strings.Contains(err.Error(), "duplicate:") {
		t.Fatalf("a repeated top-level note must be refused, got %v", err)
	}
	reply := note(t, member, 1, "Issue 201 is out", nostr.Tags{{"e", first.ID.Hex()}})
	if err := publish(t, r, reply); err != nil {
		t.Fatalf("the same text as a reply is fine: %v", err)
	}
}
