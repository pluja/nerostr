package monitor

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pluja/nerostr/api"
	"github.com/pluja/nerostr/models"
	monerorpc "github.com/pluja/nerostr/monero-rpc"
)

const (
	XmrInPico         = 1000000000000
	DefaultExpireTime = 3600
)

func MonitorInvoices(s *api.Server, tickerFrequency time.Duration, invoiceExpireTime int64) {
	ticker := time.NewTicker(tickerFrequency)
	defer ticker.Stop()

	log.Info().Msg("Starting monitor...")

	for {
		select {
		case <-ticker.C:
			log.Debug().Msg("Checking invoices...")
			err := processInvoices(s, invoiceExpireTime)
			if err != nil {
				log.Error().Err(err).Msg("Error processing invoices")
			}
		}
	}
}

func processInvoices(s *api.Server, invoiceExpireTime int64) error {
	users, err := s.Db.GetNewUsers()
	if err != nil {
		return fmt.Errorf("error getting new users: %w", err)
	}

	for _, user := range users {
		err := handleUser(s, &user, invoiceExpireTime)
		if err != nil {
			log.Error().Err(err).Msgf("Error handling user: %v", user.GetShortPubKey())
			continue
		}
	}

	return nil
}

func handleUser(s *api.Server, user *models.User, invoiceExpireTime int64) error {
	log.Debug().Msgf("Checking user: %v", user.GetShortPubKey())

	tx, err := s.MoneroRpc.GetTransactions(user.Address)
	if err != nil {
		return fmt.Errorf("error getting transactions: %w", err)
	}

	// Total in XMR float64
	total := float64(tx.TotalConfirmed+tx.TotalUnconfirmed) / float64(XmrInPico)

	log.Debug().Msgf("Total paid: %v, Admission: %v", total, user.Amount)
	if total >= user.Amount {
		err = updateUserToPaid(s, user, &tx)
	} else {
		err = checkUserExpiration(s, user, invoiceExpireTime)
	}

	return err
}

func updateUserToPaid(s *api.Server, user *models.User, tx *monerorpc.Transactions) error {
	user.Status = models.UserStatusPaid
	user.TxHash = tx.Transactions[0].TxID
	user.SetDateNow()

	err := s.Db.UpdateUser(*user)
	if err != nil {
		return fmt.Errorf("error updating user: %w", err)
	}

	return nil
}

func checkUserExpiration(s *api.Server, user *models.User, invoiceExpireTime int64) error {
	if invoiceExpireTime == 0 {
		invoiceExpireTime = DefaultExpireTime
	}

	if time.Now().Unix()-user.DateUpdated > invoiceExpireTime {
		err := s.Db.DeleteUser(user.PubKey)
		if err != nil {
			return fmt.Errorf("error deleting user: %w", err)
		}
	}

	return nil
}
