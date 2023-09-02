package utils

import (
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/rs/zerolog/log"
)

func PrasePubKey(pk string) (string, error) {
	// Check if pubkey is `npub`
	if !strings.HasPrefix(pk, "npub") {
		npub, err := nip19.EncodePublicKey(pk)
		if err != nil {
			log.Debug().Err(err).Msg("failed to encode public key")
			return "", fmt.Errorf("failed to encode public key: %w", err)
		}
		pk = npub
	} else {
		_, _, err := nip19.Decode(pk)
		if err != nil {
			return "", fmt.Errorf("failed to decode public key: %w", err)
		}
	}
	return pk, nil
}
