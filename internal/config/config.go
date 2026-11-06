package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"

	"github.com/pluja/nerostr/internal/pubkey"
	"github.com/pluja/nerostr/internal/xmr"
)

// MaxAdmission keeps every amount the relay stores far below the int64 limit of
// its database columns (about 9.2 million XMR).
const MaxAdmission = 1000 * xmr.OneXMR

type Config struct {
	ListenAddr string
	DataDir    string
	LogLevel   slog.Level

	// PublicURL is the https:// URL clients reach; the relay is served at wss:// on the same host.
	PublicURL *url.URL

	RelayName        string
	RelayDescription string
	RelayIcon        string
	RelayContact     string
	RelayOperator    *nostr.PubKey
	AdminPubKeys     []nostr.PubKey

	AdmissionAmount   xmr.Piconero
	InvoiceTTL        time.Duration
	LatePaymentWindow time.Duration
	MinConfirmations  uint64
	MaxOpenInvoices   int
	PollInterval      time.Duration

	WalletRPCURL      string
	WalletRPCUser     string
	WalletRPCPassword string

	TrustedProxies []netip.Prefix

	// LegacyAPIKey enables the v1 X-API-KEY endpoints when non-empty.
	LegacyAPIKey string

	// RelayKey signs the relay's own events (alerts). Nil means load or create one in DataDir.
	RelayKey *nostr.SecretKey

	// Inbox lets non-members send InboxKinds events that tag a member.
	Inbox      bool
	InboxKinds []nostr.Kind

	AlertPubKeys      []nostr.PubKey
	AlertAfter        time.Duration
	AlertRelays       []string
	HeartbeatURL      string
	HeartbeatInterval time.Duration
}

func (c *Config) RelayURL() string {
	if c.PublicURL.Scheme == "http" {
		return "ws://" + c.PublicURL.Host
	}
	return "wss://" + c.PublicURL.Host
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (*Config, error) {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	var errs []error
	fail := func(key string, err error) { errs = append(errs, fmt.Errorf("%s: %w", key, err)) }

	c := &Config{
		ListenAddr:        env("LISTEN_ADDR", ":8080"),
		DataDir:           env("DATA_DIR", "/data"),
		RelayName:         env("RELAY_NAME", "nerostr"),
		RelayDescription:  env("RELAY_DESCRIPTION", "A Monero-paid nostr relay. Free to read, pay once to publish."),
		RelayIcon:         env("RELAY_ICON", "/static/nerostr.webp"),
		RelayContact:      env("RELAY_CONTACT", ""),
		WalletRPCURL:      env("MONERO_WALLET_RPC_URL", "http://monero-wallet-rpc:18083/json_rpc"),
		WalletRPCUser:     env("MONERO_WALLET_RPC_USER", ""),
		WalletRPCPassword: env("MONERO_WALLET_RPC_PASSWORD", ""),
		LegacyAPIKey:      env("API_KEY", ""),
	}

	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		fail("LOG_LEVEL", err)
	}

	publicURL := env("PUBLIC_URL", "")
	if publicURL == "" {
		if host := env("NEROSTR_HOST", ""); host != "" {
			publicURL = "https://" + host
		}
	}
	if publicURL == "" {
		fail("PUBLIC_URL", errors.New("required (e.g. https://relay.example.com); NEROSTR_HOST is also accepted"))
	} else if u, err := parsePublicURL(publicURL); err != nil {
		fail("PUBLIC_URL", err)
	} else {
		c.PublicURL = u
	}

	if v := env("RELAY_PUBKEY", ""); v != "" {
		if pk, err := pubkey.Parse(v); err != nil {
			fail("RELAY_PUBKEY", err)
		} else {
			c.RelayOperator = &pk
		}
	}
	for _, v := range splitList(env("ADMIN_PUBKEYS", "")) {
		pk, err := pubkey.Parse(v)
		if err != nil {
			fail("ADMIN_PUBKEYS", fmt.Errorf("%q: %w", v, err))
			continue
		}
		c.AdminPubKeys = append(c.AdminPubKeys, pk)
	}

	switch amount, err := xmr.ParseXMR(env("ADMISSION_AMOUNT", "0.002")); {
	case err != nil:
		fail("ADMISSION_AMOUNT", err)
	case amount == 0:
		fail("ADMISSION_AMOUNT", errors.New("must be greater than zero"))
	case amount > MaxAdmission:
		fail("ADMISSION_AMOUNT", fmt.Errorf("must be at most %s XMR", MaxAdmission))
	default:
		c.AdmissionAmount = amount
	}

	c.InvoiceTTL = seconds(env("INVOICE_EXPIRE_TIME", "3600"), time.Minute, "INVOICE_EXPIRE_TIME", fail)
	c.LatePaymentWindow = seconds(env("LATE_PAYMENT_WINDOW", strconv.Itoa(30*24*3600)), time.Hour, "LATE_PAYMENT_WINDOW", fail)
	c.PollInterval = seconds(env("PAYMENT_POLL_INTERVAL", "20"), 5*time.Second, "PAYMENT_POLL_INTERVAL", fail)

	if n, err := strconv.ParseUint(env("MIN_CONFIRMATIONS", "1"), 10, 64); err != nil || n > 60 {
		fail("MIN_CONFIRMATIONS", errors.New("must be an integer between 0 and 60"))
	} else {
		c.MinConfirmations = n
	}
	if n, err := strconv.Atoi(env("MAX_OPEN_INVOICES", "500")); err != nil || n < 1 {
		fail("MAX_OPEN_INVOICES", errors.New("must be a positive integer"))
	} else {
		c.MaxOpenInvoices = n
	}

	for _, v := range splitList(env("TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7")) {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			if a, aerr := netip.ParseAddr(v); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				fail("TRUSTED_PROXIES", fmt.Errorf("%q is not an IP or CIDR", v))
				continue
			}
		}
		c.TrustedProxies = append(c.TrustedProxies, p.Masked())
	}

	if (c.WalletRPCUser == "") != (c.WalletRPCPassword == "") {
		fail("MONERO_WALLET_RPC_USER", errors.New("MONERO_WALLET_RPC_USER and MONERO_WALLET_RPC_PASSWORD must be set together"))
	}
	if u, err := url.Parse(c.WalletRPCURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail("MONERO_WALLET_RPC_URL", errors.New("must be an http(s) URL"))
	}

	if len(c.LegacyAPIKey) > 0 && len(c.LegacyAPIKey) < 16 {
		fail("API_KEY", errors.New("must be at least 16 characters"))
	}

	if v := env("RELAY_SECRET_KEY", ""); v != "" {
		if sk, err := parseSecretKey(v); err != nil {
			fail("RELAY_SECRET_KEY", err)
		} else {
			c.RelayKey = &sk
		}
	}

	switch strings.ToLower(env("INBOX", "true")) {
	case "true", "1", "yes", "on":
		c.Inbox = true
	case "false", "0", "no", "off":
	default:
		fail("INBOX", errors.New("must be true or false"))
	}
	for _, v := range splitList(env("INBOX_KINDS", "1,4,6,7,16,1059,1111,9735")) {
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			fail("INBOX_KINDS", fmt.Errorf("%q is not an event kind", v))
			continue
		}
		c.InboxKinds = append(c.InboxKinds, nostr.Kind(n))
	}

	c.AlertPubKeys = c.AdminPubKeys
	if v := env("ALERT_PUBKEYS", ""); v != "" {
		c.AlertPubKeys = nil
		for _, v := range splitList(v) {
			pk, err := pubkey.Parse(v)
			if err != nil {
				fail("ALERT_PUBKEYS", fmt.Errorf("%q: %w", v, err))
				continue
			}
			c.AlertPubKeys = append(c.AlertPubKeys, pk)
		}
	}
	c.AlertAfter = seconds(env("ALERT_AFTER", "600"), time.Minute, "ALERT_AFTER", fail)
	for _, v := range splitList(env("ALERT_RELAYS", "wss://purplepag.es,wss://relay.damus.io,wss://nos.lol")) {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
			fail("ALERT_RELAYS", fmt.Errorf("%q is not a ws(s):// URL", v))
			continue
		}
		c.AlertRelays = append(c.AlertRelays, v)
	}
	if v := env("HEARTBEAT_URL", ""); v != "" {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail("HEARTBEAT_URL", errors.New("must be an http(s) URL"))
		} else {
			c.HeartbeatURL = v
		}
	}
	c.HeartbeatInterval = seconds(env("HEARTBEAT_INTERVAL", "300"), time.Minute, "HEARTBEAT_INTERVAL", fail)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func parseSecretKey(s string) (nostr.SecretKey, error) {
	if strings.HasPrefix(s, "nsec1") {
		prefix, value, err := nip19.Decode(s)
		if err != nil || prefix != "nsec" {
			return nostr.SecretKey{}, errors.New("invalid nsec")
		}
		return value.(nostr.SecretKey), nil
	}
	sk, err := nostr.SecretKeyFromHex(s)
	if err != nil {
		return nostr.SecretKey{}, errors.New("must be an nsec or 64-char hex secret key")
	}
	return sk, nil
}

func parsePublicURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("must start with https:// (or http:// for local testing)")
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be a bare origin like https://relay.example.com")
	}
	u.Path = ""
	return u, nil
}

func seconds(v string, min time.Duration, key string, fail func(string, error)) time.Duration {
	n, err := strconv.ParseInt(v, 10, 64)
	d := time.Duration(n) * time.Second
	if err != nil || d < min {
		fail(key, fmt.Errorf("must be a number of seconds >= %d", int(min.Seconds())))
		return 0
	}
	return d
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
