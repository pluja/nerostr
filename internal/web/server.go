package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/pluja/nerostr/internal/config"
	"github.com/pluja/nerostr/internal/payments"
	"github.com/pluja/nerostr/internal/pubkey"
	"github.com/pluja/nerostr/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg      *config.Config
	store    *store.Store
	payments *payments.Service
	log      *slog.Logger
	version  string
	pages    map[string]*template.Template

	invoiceLimiter *limiter
	checkLimiter   *limiter
	apiLimiter     *limiter
}

func New(cfg *config.Config, st *store.Store, pay *payments.Service, log *slog.Logger, version string) (*Server, error) {
	s := &Server{
		cfg:            cfg,
		store:          st,
		payments:       pay,
		log:            log,
		version:        version,
		pages:          map[string]*template.Template{},
		invoiceLimiter: newLimiter(2*time.Minute, 5),
		checkLimiter:   newLimiter(5*time.Second, 3),
		apiLimiter:     newLimiter(time.Second, 30),
	}
	for _, page := range []string{"index", "user"} {
		t, err := template.New("base.html").ParseFS(templateFS, "templates/base.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	return s, nil
}

// Register adds the web routes to mux. The relay owns "/" for websocket,
// NIP-11 and NIP-86 requests; everything else falls through to these handlers.
func (s *Server) Register(mux *http.ServeMux) {
	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(7*24*time.Hour, fileServer)))
	mux.Handle("GET /favicon.ico", cacheFor(7*24*time.Hour, fileServer))

	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /user", s.handleCreateInvoice)
	mux.HandleFunc("GET /user/{pkey}", s.handleUser)
	mux.HandleFunc("GET /user/{pkey}/qr.png", s.handleQR)
	mux.HandleFunc("POST /user/{pkey}/check", s.handleCheck)
	mux.HandleFunc("GET /api/user/{pkey}", s.handleStatusJSON)
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// v1 compatibility.
	mux.HandleFunc("GET /api/status/{pkey}", s.handleLegacyStatus)
	if s.cfg.LegacyAPIKey != "" {
		mux.HandleFunc("POST /api/user/{pkey}", s.handleLegacyAdd)
		mux.HandleFunc("DELETE /api/user/{pkey}", s.handleLegacyDelete)
	}
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	value := "public, max-age=" + strconv.FormatInt(int64(d.Seconds()), 10)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}

// --- pages ---

type basePage struct {
	Title    string
	Name     string
	RelayURL string
	Version  string
	Amount   string
	Refresh  bool
}

func (s *Server) base(title string) basePage {
	return basePage{
		Title:    title,
		Name:     s.cfg.RelayName,
		RelayURL: s.cfg.RelayURL(),
		Version:  s.version,
		Amount:   s.cfg.AdmissionAmount.String(),
	}
}

var errorMessages = map[string]string{
	"invalid-key": "That doesn't look like a nostr public key. Paste your npub (or hex key).",
	"busy":        payments.ErrBusy.Error() + ".",
	"wallet":      payments.ErrWallet.Error() + ".",
	"slow-down":   "Too many requests from your connection. Please wait a minute and try again.",
	"banned":      payments.ErrBanned.Error() + ".",
}

type indexPage struct {
	basePage
	Members int
	Error   string
	Inbox   bool
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	members, err := s.store.CountMembers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, http.StatusOK, "index", indexPage{
		basePage: s.base(s.cfg.RelayName),
		Members:  members,
		Error:    errorMessages[r.URL.Query().Get("error")],
		Inbox:    s.cfg.Inbox,
	})
}

func (s *Server) handleCreateInvoice(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pk, err := pubkey.Parse(r.PostForm.Get("pkey"))
	if err != nil {
		redirect(w, r, "/?error=invalid-key")
		return
	}
	if !s.invoiceLimiter.allow(clientIP(r)) {
		redirect(w, r, "/?error=slow-down")
		return
	}
	_, err = s.payments.Invoice(r.Context(), pk)
	switch {
	case err == nil, errors.Is(err, payments.ErrAlreadyMember), errors.Is(err, payments.ErrBanned):
		redirect(w, r, "/user/"+pubkey.Npub(pk))
	case errors.Is(err, payments.ErrBusy):
		redirect(w, r, "/?error=busy")
	case errors.Is(err, payments.ErrWallet):
		redirect(w, r, "/?error=wallet")
	default:
		s.serverError(w, err)
	}
}

type userPage struct {
	basePage
	Npub    string
	Short   string
	State   string // member, banned, invoice, none
	Member  *store.Member
	Invoice *invoiceView
}

type invoiceView struct {
	Address       string
	URI           template.URL
	Amount        string
	Seen          string
	Confirmed     string
	Remaining     string
	SeenAny       bool
	Pending       bool
	Expired       bool
	ExpiresAt     int64
	Confirmations uint64
	LateDays      int
	StatusURL     string
	QRURL         string
	CheckURL      string
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		redirect(w, r, "/?error=invalid-key")
		return
	}
	npub := pubkey.Npub(pk)
	if r.PathValue("pkey") != npub {
		redirect(w, r, "/user/"+npub)
		return
	}

	st, err := s.status(r.Context(), pk)
	if err != nil {
		s.serverError(w, err)
		return
	}
	page := userPage{basePage: s.base(pubkey.Short(pk) + " · " + s.cfg.RelayName), Npub: npub, Short: pubkey.Short(pk)}
	switch {
	case st.Banned:
		page.State = "banned"
	case st.member != nil:
		page.State = "member"
		page.Member = st.member
	case st.invoice != nil && !st.invoice.Expired(time.Now()):
		page.State = "invoice"
		page.Invoice = s.invoiceView(npub, st.invoice)
		page.Refresh = true
	case st.invoice != nil:
		page.State = "expired"
		page.Invoice = s.invoiceView(npub, st.invoice)
		page.Refresh = page.Invoice.SeenAny
	default:
		page.State = "none"
	}
	s.render(w, http.StatusOK, "user", page)
}

func (s *Server) invoiceView(npub string, inv *store.Invoice) *invoiceView {
	return &invoiceView{
		Address: inv.Address,
		// html/template rejects the monero: scheme; the URI is built only from a
		// wallet-issued address and URL-encoded parameters.
		URI:           template.URL(moneroURI(inv)), //nolint:gosec // built from a wallet address and URL-encoded values
		Amount:        inv.Amount.String(),
		Seen:          inv.Seen.String(),
		Confirmed:     inv.Confirmed.String(),
		Remaining:     inv.Amount.Sub(inv.Seen).String(),
		SeenAny:       inv.Seen > 0,
		Pending:       inv.Seen >= inv.Amount && inv.Confirmed < inv.Amount,
		Expired:       inv.Expired(time.Now()),
		ExpiresAt:     inv.ExpiresAt.Unix(),
		Confirmations: s.payments.Config().MinConfirmations,
		LateDays:      int(s.payments.Config().LatePaymentWindow.Hours() / 24),
		StatusURL:     "/api/user/" + npub,
		QRURL:         "/user/" + npub + "/qr.png",
		CheckURL:      "/user/" + npub + "/check",
	}
}

func moneroURI(inv *store.Invoice) string {
	q := url.Values{}
	q.Set("tx_amount", inv.Amount.String())
	q.Set("tx_description", "nostr relay admission")
	return "monero:" + inv.Address + "?" + q.Encode()
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv, err := s.store.LatestInvoice(r.Context(), pk)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	png, err := qrcode.Encode(moneroURI(&inv), qrcode.Medium, 320)
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(png) //nolint:gosec // generated PNG bytes served as image/png
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		redirect(w, r, "/?error=invalid-key")
		return
	}
	if s.checkLimiter.allow(clientIP(r)) {
		s.payments.Kick()
	}
	if r.Header.Get("Accept") == "application/json" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	redirect(w, r, "/user/"+pubkey.Npub(pk))
}

// --- JSON ---

type userStatus struct {
	Member  bool           `json:"member"`
	Banned  bool           `json:"banned"`
	Invoice *invoiceStatus `json:"invoice,omitempty"`

	member  *store.Member
	invoice *store.Invoice
}

type invoiceStatus struct {
	Address               string `json:"address"`
	Amount                string `json:"amount"`
	Seen                  string `json:"seen"`
	Confirmed             string `json:"confirmed"`
	Remaining             string `json:"remaining"`
	ConfirmationsRequired uint64 `json:"confirmations_required"`
	ExpiresAt             int64  `json:"expires_at"`
	Expired               bool   `json:"expired"`
	Paid                  bool   `json:"paid"`
}

func (s *Server) status(ctx context.Context, pk nostr.PubKey) (userStatus, error) {
	var st userStatus
	var err error
	if st.Banned, err = s.store.IsBannedPubKey(ctx, pk); err != nil {
		return st, err
	}
	m, err := s.store.GetMember(ctx, pk)
	if err == nil {
		st.Member, st.member = true, &m
	} else if !errors.Is(err, store.ErrNotFound) {
		return st, err
	}
	inv, err := s.store.LatestInvoice(ctx, pk)
	if err == nil {
		st.invoice = &inv
		st.Invoice = &invoiceStatus{
			Address:               inv.Address,
			Amount:                inv.Amount.String(),
			Seen:                  inv.Seen.String(),
			Confirmed:             inv.Confirmed.String(),
			Remaining:             inv.Amount.Sub(inv.Seen).String(),
			ConfirmationsRequired: s.payments.Config().MinConfirmations,
			ExpiresAt:             inv.ExpiresAt.Unix(),
			Expired:               inv.Expired(time.Now()),
			Paid:                  inv.Paid(),
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return st, err
	}
	return st, nil
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": pubkey.ErrInvalid.Error()})
		return
	}
	if !s.apiLimiter.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down"})
		return
	}
	st, err := s.status(r.Context(), pk)
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ps := s.payments.Status()
	pay := map[string]any{
		"healthy":       s.payments.Healthy(),
		"wallet_height": ps.WalletHeight,
		"watching":      ps.Watching,
	}
	if !ps.LastCheck.IsZero() {
		pay["last_check"] = ps.LastCheck.Unix()
	}
	if ps.LastError != "" {
		pay["error"] = "wallet rpc unavailable, see logs"
	} else if problem := s.payments.Problem(); problem != "" {
		pay["error"] = problem
	}
	body := map[string]any{"version": s.version, "database": "ok", "payments": pay}
	code := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		body["database"] = "unavailable"
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, body)
}

// --- v1 compatibility ---

func (s *Server) handleLegacyStatus(w http.ResponseWriter, r *http.Request) {
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": -1, "action": "reject"})
		return
	}
	member, err := s.store.IsMember(r.Context(), pk)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if member {
		writeJSON(w, http.StatusOK, map[string]any{"status": 2, "action": "accept"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": 0, "action": "reject"})
}

func (s *Server) legacyAuthorized(r *http.Request) bool {
	got := r.Header.Get("X-API-KEY")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.LegacyAPIKey)) == 1
}

func (s *Server) handleLegacyAdd(w http.ResponseWriter, r *http.Request) {
	if !s.legacyAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid apikey"})
		return
	}
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "error parsing pubkey"})
		return
	}
	if err := s.store.AddMember(r.Context(), pk, store.SourceAdmin, "v1 api"); err != nil {
		s.serverError(w, err)
		return
	}
	s.log.Info("member added via v1 api", "pubkey", pubkey.Short(pk))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLegacyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.legacyAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid apikey"})
		return
	}
	pk, err := pubkey.Parse(r.PathValue("pkey"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "error parsing pubkey"})
		return
	}
	if err := s.store.RemoveMember(r.Context(), pk); err != nil {
		s.serverError(w, err)
		return
	}
	s.log.Info("member removed via v1 api", "pubkey", pubkey.Short(pk))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers ---

func (s *Server) render(w http.ResponseWriter, code int, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := s.pages[page].Execute(w, data); err != nil {
		s.log.Error("rendering page", "page", page, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, "internal error, please try again later", http.StatusInternalServerError)
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // callers only pass paths on this site
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
