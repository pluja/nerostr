package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/nip19"

	"github.com/pluja/nerostr/internal/alerts"
	"github.com/pluja/nerostr/internal/config"
	"github.com/pluja/nerostr/internal/importer"
	"github.com/pluja/nerostr/internal/payments"
	"github.com/pluja/nerostr/internal/relay"
	"github.com/pluja/nerostr/internal/store"
	"github.com/pluja/nerostr/internal/wallet"
	"github.com/pluja/nerostr/internal/web"
)

var version = "dev"

const usage = `nerostr: a nostr relay paid with Monero

usage:
  nerostr [serve]            run the relay (configured through environment variables)
  nerostr import [file]      import events from a JSONL file or stdin (e.g. strfry export)
  nerostr migrate-v1 <dir>   import paid users from a nerostr v1 database directory
  nerostr healthcheck        exit 0 if the local server reports healthy
  nerostr version            print the version
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "import":
		err = importEvents(os.Args[2:])
	case "migrate-v1":
		err = migrateV1(os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func dataDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	return "/data"
}

func openStores(dir string) (*store.Store, *lmdb.LMDBBackend, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(filepath.Join(dir, "nerostr.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("opening database: %w", err)
	}
	events := &lmdb.LMDBBackend{Path: filepath.Join(dir, "events")}
	if err := events.Init(); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("opening event store: %w", err)
	}
	return st, events, nil
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := newLogger(cfg.LogLevel)
	relay.Version = version

	st, events, err := openStores(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	defer events.Close()

	if cfg.RelayKey == nil {
		key, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "relay.key"))
		if err != nil {
			return fmt.Errorf("relay key: %w", err)
		}
		cfg.RelayKey = &key
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w := wallet.New(cfg.WalletRPCURL, cfg.WalletRPCUser, cfg.WalletRPCPassword)
	if major, minor, err := w.Version(ctx); err != nil {
		log.Warn("wallet rpc not reachable yet; payments resume once it is", "err", err)
	} else {
		log.Info("connected to wallet rpc", "rpc_version", fmt.Sprintf("%d.%d", major, minor))
	}

	pay := payments.New(payments.Config{
		Amount:            cfg.AdmissionAmount,
		InvoiceTTL:        cfg.InvoiceTTL,
		LatePaymentWindow: cfg.LatePaymentWindow,
		MinConfirmations:  cfg.MinConfirmations,
		MaxOpenInvoices:   cfg.MaxOpenInvoices,
		PollInterval:      cfg.PollInterval,
	}, st, w, log.With("component", "payments"))

	rl := relay.New(cfg, st, events, log.With("component", "relay"))
	site, err := web.New(cfg, st, pay, log.With("component", "web"), version)
	if err != nil {
		return err
	}
	site.Register(rl.Router())

	handler := web.RealIP(cfg.TrustedProxies, web.SecurityHeaders(web.LimitBody(1<<20, rl)))
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	go pay.Run(ctx)
	go backupLoop(ctx, st, filepath.Join(cfg.DataDir, "backups"), log)

	monitor := &alerts.Monitor{
		Name:         cfg.PublicURL.Host,
		Problem:      pay.Problem,
		After:        cfg.AlertAfter,
		HeartbeatURL: cfg.HeartbeatURL,
		Heartbeat:    cfg.HeartbeatInterval,
		Log:          log.With("component", "alerts"),
	}
	if len(cfg.AlertPubKeys) > 0 {
		deliverLocal := func(_ context.Context, evt nostr.Event) error {
			if err := events.SaveEvent(evt); err != nil && !errors.Is(err, eventstore.ErrDupEvent) {
				return err
			}
			rl.BroadcastEvent(evt)
			return nil
		}
		monitor.Notify = alerts.NewNotifier(*cfg.RelayKey, cfg.AlertPubKeys, cfg.AlertRelays, cfg.RelayURL(),
			deliverLocal, log.With("component", "alerts")).Notify
	}
	go monitor.Run(ctx)

	errc := make(chan error, 1)
	go func() {
		log.Info("nerostr listening", "version", version, "addr", cfg.ListenAddr, "relay", cfg.RelayURL(),
			"admission", cfg.AdmissionAmount.String()+" XMR", "min_confirmations", cfg.MinConfirmations,
			"admins", len(cfg.AdminPubKeys), "inbox", cfg.Inbox, "alert_recipients", len(cfg.AlertPubKeys),
			"heartbeat", cfg.HeartbeatURL != "", "relay_pubkey", nip19.EncodeNpub(cfg.RelayKey.Public()))
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	rl.DisconnectAll()
	// khatru handles each websocket message in its own goroutine, so give
	// in-flight ones a moment to finish before the deferred LMDB close.
	time.Sleep(time.Second)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// backupLoop keeps a week of daily snapshots of the member database.
func backupLoop(ctx context.Context, st *store.Store, dir string, log *slog.Logger) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Error("cannot create backup directory", "err", err)
		return
	}
	snapshot := func() {
		path := filepath.Join(dir, "nerostr-"+time.Now().UTC().Format("2006-01-02")+".db")
		if _, err := os.Stat(path); err == nil {
			return
		}
		if err := st.Backup(ctx, path); err != nil {
			log.Error("database backup failed", "err", err)
			return
		}
		entries, _ := filepath.Glob(filepath.Join(dir, "nerostr-*.db"))
		sort.Strings(entries)
		for len(entries) > 7 {
			os.Remove(entries[0])
			entries = entries[1:]
		}
	}
	snapshot()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snapshot()
		}
	}
}

func importEvents(args []string) error {
	log := newLogger(slog.LevelInfo)
	var in io.Reader = os.Stdin
	if len(args) > 0 && args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	st, events, err := openStores(dataDir())
	if err != nil {
		return err
	}
	defer st.Close()
	defer events.Close()

	start := time.Now()
	stats, err := importer.ImportEvents(in, events, log)
	log.Info("import finished", "imported", stats.Imported, "duplicates", stats.Duplicates,
		"invalid", stats.Invalid, "skipped_ephemeral", stats.Skipped, "skipped_repeats", stats.Repeats, "took", time.Since(start).Round(time.Second))
	return err
}

func migrateV1(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: nerostr migrate-v1 <path to v1 nerostr-db directory>")
	}
	log := newLogger(slog.LevelInfo)
	st, events, err := openStores(dataDir())
	if err != nil {
		return err
	}
	defer st.Close()
	defer events.Close()

	stats, err := importer.MigrateV1(context.Background(), args[0], filepath.Join(dataDir(), "tmp"), st, log)
	log.Info("v1 migration finished", "members_imported", stats.Imported,
		"unpaid_skipped", stats.Pending, "invalid", stats.Invalid)
	return err
}

// loadOrCreateKey keeps the relay's own nostr identity stable across restarts.
func loadOrCreateKey(path string) (nostr.SecretKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		return nostr.SecretKeyFromHex(strings.TrimSpace(string(b)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nostr.SecretKey{}, err
	}
	key := nostr.Generate()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nostr.SecretKey{}, err
	}
	if _, err := f.WriteString(key.Hex() + "\n"); err != nil {
		f.Close()
		return nostr.SecretKey{}, err
	}
	return key, f.Close()
}

func healthcheck() error {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
