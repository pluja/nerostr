package pubkey

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

func TestParse(t *testing.T) {
	sk := nostr.Generate()
	pk := sk.Public()
	npub := nip19.EncodeNpub(pk)
	nprofile := nip19.EncodeNprofile(pk, []string{"wss://example.com"})

	for _, in := range []string{
		pk.Hex(),
		strings.ToUpper(pk.Hex()),
		npub,
		"nostr:" + npub,
		"  " + npub + "\n",
		nprofile,
	} {
		got, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got != pk {
			t.Fatalf("Parse(%q) = %s, want %s", in, got.Hex(), pk.Hex())
		}
	}
}

func TestParseRejects(t *testing.T) {
	nevent := nip19.EncodeNevent(nostr.ID{1}, nil, nostr.ZeroPK)
	nsec := nip19.EncodeNsec(nostr.Generate())
	for _, in := range []string{
		"",
		"npub1",
		"#",
		"`",
		strings.Repeat("z", 64),
		strings.Repeat("0", 64), // not on the curve
		nevent,
		nsec,
		strings.Repeat("a", 2000),
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
}
