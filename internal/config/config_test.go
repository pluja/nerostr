package config

import (
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadLegacyEnv(t *testing.T) {
	c, err := load(envOf(map[string]string{
		"NEROSTR_HOST":        "xmr.usenostr.org",
		"ADMISSION_AMOUNT":    "0.005",
		"INVOICE_EXPIRE_TIME": "7200",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.PublicURL.String(); got != "https://xmr.usenostr.org" {
		t.Errorf("PublicURL = %q", got)
	}
	if c.RelayURL() != "wss://xmr.usenostr.org" {
		t.Errorf("RelayURL = %q", c.RelayURL())
	}
	if c.AdmissionAmount.String() != "0.005" {
		t.Errorf("AdmissionAmount = %s", c.AdmissionAmount)
	}
	if c.InvoiceTTL != 2*time.Hour {
		t.Errorf("InvoiceTTL = %s", c.InvoiceTTL)
	}
	if c.MinConfirmations != 1 || c.LatePaymentWindow != 30*24*time.Hour {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if len(c.TrustedProxies) == 0 {
		t.Error("expected default trusted proxies")
	}
}

func TestLoadAdmins(t *testing.T) {
	pk := nostr.Generate().Public()
	c, err := load(envOf(map[string]string{
		"PUBLIC_URL":    "https://relay.example.com/",
		"ADMIN_PUBKEYS": nip19.EncodeNpub(pk) + ", " + pk.Hex(),
		"RELAY_PUBKEY":  pk.Hex(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AdminPubKeys) != 2 || c.AdminPubKeys[0] != pk || *c.RelayOperator != pk {
		t.Errorf("admins not parsed: %v", c.AdminPubKeys)
	}
	if c.PublicURL.String() != "https://relay.example.com" {
		t.Errorf("PublicURL = %q", c.PublicURL)
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := load(envOf(map[string]string{
		"PUBLIC_URL":             "ftp://x/y",
		"ADMISSION_AMOUNT":       "0",
		"ADMIN_PUBKEYS":          "npub1nope",
		"MIN_CONFIRMATIONS":      "-1",
		"TRUSTED_PROXIES":        "not-an-ip",
		"MONERO_WALLET_RPC_USER": "only-user",
		"API_KEY":                "short",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"PUBLIC_URL", "ADMISSION_AMOUNT", "ADMIN_PUBKEYS", "MIN_CONFIRMATIONS", "TRUSTED_PROXIES", "MONERO_WALLET_RPC_USER", "API_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestRequiresPublicURL(t *testing.T) {
	if _, err := load(envOf(nil)); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL") {
		t.Fatalf("expected PUBLIC_URL error, got %v", err)
	}
}

func TestAlertAndInboxOptions(t *testing.T) {
	admin := nostr.Generate().Public()
	sk := nostr.Generate()

	c, err := load(envOf(map[string]string{
		"PUBLIC_URL":    "https://relay.example.com",
		"ADMIN_PUBKEYS": admin.Hex(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Inbox || len(c.InboxKinds) == 0 || c.RelayKey != nil {
		t.Fatalf("defaults: inbox=%v kinds=%v key=%v", c.Inbox, c.InboxKinds, c.RelayKey)
	}
	if len(c.AlertPubKeys) != 1 || c.AlertPubKeys[0] != admin {
		t.Fatalf("alerts should default to admins: %v", c.AlertPubKeys)
	}
	if c.AlertAfter != 10*time.Minute || len(c.AlertRelays) == 0 || c.HeartbeatURL != "" {
		t.Fatalf("alert defaults: %+v", c)
	}

	c, err = load(envOf(map[string]string{
		"PUBLIC_URL":       "https://relay.example.com",
		"RELAY_SECRET_KEY": nip19.EncodeNsec(sk),
		"INBOX":            "off",
		"INBOX_KINDS":      "1059",
		"ALERT_PUBKEYS":    nip19.EncodeNpub(admin),
		"HEARTBEAT_URL":    "https://hc-ping.com/abc",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayKey == nil || *c.RelayKey != sk || c.Inbox || len(c.InboxKinds) != 1 || c.InboxKinds[0] != 1059 {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if c.HeartbeatURL != "https://hc-ping.com/abc" || c.AlertPubKeys[0] != admin {
		t.Fatalf("alert overrides: %+v", c)
	}

	_, err = load(envOf(map[string]string{
		"PUBLIC_URL":       "https://relay.example.com",
		"RELAY_SECRET_KEY": "nope",
		"INBOX":            "maybe",
		"INBOX_KINDS":      "x",
		"ALERT_RELAYS":     "https://not-a-relay",
		"HEARTBEAT_URL":    "ftp://x",
	}))
	for _, key := range []string{"RELAY_SECRET_KEY", "INBOX:", "INBOX_KINDS", "ALERT_RELAYS", "HEARTBEAT_URL"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("expected %s error, got %v", key, err)
		}
	}
}
