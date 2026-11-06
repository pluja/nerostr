package pubkey

import (
	"errors"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

var ErrInvalid = errors.New("not a valid nostr public key (expected npub, nprofile or 64-char hex)")

// Parse accepts hex, npub or nprofile (optionally prefixed with "nostr:") and
// returns a public key that is guaranteed to be a valid secp256k1 x-only point.
func Parse(s string) (nostr.PubKey, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "nostr:")
	if len(s) > 512 {
		return nostr.ZeroPK, ErrInvalid
	}

	if len(s) == 64 {
		pk, err := nostr.PubKeyFromHex(strings.ToLower(s))
		if err != nil {
			return nostr.ZeroPK, ErrInvalid
		}
		return pk, nil
	}

	prefix, value, err := nip19.Decode(strings.ToLower(s))
	if err != nil {
		return nostr.ZeroPK, ErrInvalid
	}
	var pk nostr.PubKey
	switch prefix {
	case "npub":
		pk = value.(nostr.PubKey)
	case "nprofile":
		pk = value.(nostr.ProfilePointer).PublicKey
	default:
		return nostr.ZeroPK, ErrInvalid
	}
	if _, err := nostr.PubKeyFromHex(pk.Hex()); err != nil {
		return nostr.ZeroPK, ErrInvalid
	}
	return pk, nil
}

func Npub(pk nostr.PubKey) string {
	return nip19.EncodeNpub(pk)
}

// Short renders an npub as "npub1abcd…wxyz" for display.
func Short(pk nostr.PubKey) string {
	n := Npub(pk)
	return n[:12] + "…" + n[len(n)-6:]
}
