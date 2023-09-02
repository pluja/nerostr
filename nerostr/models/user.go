package models

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/rs/zerolog/log"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/pluja/nerostr/utils"
)

type User struct {
	PubKey      string  `json:"pubkey"`
	Address     string  `json:"address"`
	Status      int     `json:"status"`
	Amount      float64 `json:"amount"`
	TxHash      string  `json:"txhash"`
	DateUpdated int64   `json:"date_updated"`
}

func (u *User) SetStatus(status int) {
	u.Status = status
}

func (u *User) SetDateNow() {
	u.DateUpdated = time.Now().Unix()
}

func (u *User) SetPubKey(pubkey string) error {
	npub, err := utils.PrasePubKey(pubkey)
	if err != nil {
		log.Error().Err(err).Msg("Error parsing pubkey")
		return err
	}
	log.Printf("SetPubKey: %s", npub)
	u.PubKey = npub
	return nil
}

func (u *User) GetHexPubKey() string {
	_, value, err := nip19.Decode(u.PubKey)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%x", value)
}

func (u *User) SetAmount(amount float64) {
	u.Amount = amount
}

func (u *User) SetTxHash(txhash string) {
	u.TxHash = txhash
}

func (u *User) SetAddress(address string) {
	u.Address = address
}

func (u *User) GetShortPubKey() string {
	return fmt.Sprintf("%s...%s", u.PubKey[:10], u.PubKey[len(u.PubKey)-10:])
}

func (u *User) ParseStatus() string {
	switch u.Status {
	case UserStatusNew:
		return "pending payment"
	case UserStatusReceived:
		return "payment received"
	case UserStatusPaid:
		return "accepted"
	case UserStatusError:
		return "error"
	default:
		return "unknown"
	}
}

func (u *User) GetAddreessQrCodeDataUrl() string {
	png, err := qrcode.Encode(fmt.Sprintf("monero:%s?tx_amount=%f&tx_description=nerostr_payment", u.Address, u.Amount), qrcode.Medium, 256)
	if err != nil {
		fmt.Printf("could not generate QRCode: %v", err)
		return "error"
	}
	dataUrl := base64.StdEncoding.EncodeToString(png)
	return dataUrl
}

const (
	UserStatusNew      = iota
	UserStatusReceived = 1
	UserStatusPaid     = 2
	UserStatusError    = -1
)
