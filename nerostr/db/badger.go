package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/rs/zerolog/log"

	"github.com/pluja/nerostr/models"
)

/*User Model example

type User struct {
	PubKey  string `json:"pubkey"`
	Address string `json:"address"`
	Status  int    `json:"status"`
	Amount  uint64 `json:"amount"`
	TxHash  string `json:"txhash"`
}
*/

type BadgerDB struct {
	db *badger.DB
}

func NewBadgerDB(dir string) (BadgerDB, error) {
	db, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		return BadgerDB{}, err
	}
	return BadgerDB{db: db}, nil
}

func (bdb BadgerDB) NewUser(user models.User) error {
	// Convert user to []byte
	json, err := json.Marshal(user)
	if err != nil {
		return err
	}
	return bdb.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(user.PubKey), json)
	})
}

func (bdb BadgerDB) GetUser(pubkey string) (models.User, error) {
	// Check if pubkey is `npub`
	if !strings.HasPrefix(pubkey, "npub") {
		// Convert to npub
		npub, err := nip19.EncodePublicKey(pubkey)
		if err != nil {
			log.Debug().Err(err).Msg("failed to encode public key")
			return models.User{}, fmt.Errorf("failed to encode public key: %w", err)
		}
		pubkey = npub
	}
	var user models.User
	err := bdb.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(pubkey))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			err = json.Unmarshal(val, &user)
			if err != nil {
				return err
			}
			return nil
		})
	})
	return user, err
}

func (bdb BadgerDB) UpdateUser(user models.User) error {
	// Convert user to []byte
	json, err := json.Marshal(user)
	if err != nil {
		return err
	}
	return bdb.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(user.PubKey), json)
	})
}

func (bdb BadgerDB) DeleteUser(pubkey string) error {
	return bdb.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(pubkey))
	})
}

func (bdb BadgerDB) Close() error {
	return bdb.db.Close()
}

func (bdb BadgerDB) GetNewUsers() ([]models.User, error) {
	var users []models.User

	err := bdb.db.View(func(txn *badger.Txn) error {
		// Set PrefetchSize to optimize the number of calls to the database.
		opts := badger.DefaultIteratorOptions
		opts.PrefetchSize = 10
		it := txn.NewIterator(opts)
		defer it.Close()

		// Iterate over each key in the database.
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var user models.User
			err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &user)
			})
			if err != nil {
				return err
			}
			if user.Status == models.UserStatusNew {
				users = append(users, user)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return users, nil
}
