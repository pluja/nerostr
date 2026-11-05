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
