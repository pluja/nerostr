package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/dgraph-io/badger/v4"

	"github.com/pluja/nerostr/internal/flood"
	"github.com/pluja/nerostr/internal/pubkey"
	"github.com/pluja/nerostr/internal/store"
)

const maxLine = 4 << 20

type EventStats struct {
	Imported, Duplicates, Invalid, Skipped, Repeats int
}

// ImportEvents reads newline-delimited JSON events (as written by `strfry export`)
// and stores the valid ones. Every event's id and signature are re-verified, and
// the relay's flood rule applies: a top-level note repeating the same author's
// text within an hour is skipped, so the first copy is the one kept.
func ImportEvents(r io.Reader, events eventstore.Store, log *slog.Logger) (EventStats, error) {
	var st EventStats
	repeats := flood.NewGuard(events)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var evt nostr.Event
		if err := json.Unmarshal(raw, &evt); err != nil {
			st.Invalid++
			log.Warn("skipping unparseable line", "line", line, "err", err)
			continue
		}
		if !evt.CheckID() || !evt.VerifySignature() {
			st.Invalid++
			continue
		}
		if evt.Kind.IsEphemeral() {
			st.Skipped++
			continue
		}
		if repeats.Repeat(evt) {
			st.Repeats++
			continue
		}
		var err error
		if evt.Kind.IsRegular() {
			err = events.SaveEvent(evt)
		} else if exists(events, evt.ID) {
			err = eventstore.ErrDupEvent
		} else {
			_, err = events.ReplaceEvent(evt)
		}
		switch {
		case err == nil:
			st.Imported++
		case errors.Is(err, eventstore.ErrDupEvent):
			st.Duplicates++
		default:
			return st, fmt.Errorf("line %d: saving event %s: %w", line, evt.ID.Hex(), err)
		}
		if line%50000 == 0 {
			log.Info("import progress", "lines", line, "imported", st.Imported)
		}
	}
	if err := sc.Err(); err != nil {
		return st, fmt.Errorf("line %d: %w", line+1, err)
	}
	return st, nil
}

func exists(events eventstore.Store, id nostr.ID) bool {
	for range events.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return true
	}
	return false
}

type MemberStats struct {
	Imported, Pending, Invalid int
}

// v1User mirrors the JSON values nerostr v1 stored in Badger, keyed by npub.
type v1User struct {
	PubKey string `json:"pubkey"`
	Status int    `json:"status"`
	TxHash string `json:"txhash"`
}

const v1StatusPaid = 2

// MigrateV1 copies paid users from a nerostr v1 Badger database. Unpaid v1
// invoices are not migrated because their subaddresses are unknown to v2.
// The source directory is never modified; scratchDir holds a temporary copy
// when the database needs crash recovery first.
func MigrateV1(ctx context.Context, dir, scratchDir string, st *store.Store, log *slog.Logger) (MemberStats, error) {
	var stats MemberStats
	db, cleanup, err := openV1(dir, scratchDir, log)
	if err != nil {
		return stats, err
	}
	defer cleanup()

	var paid []v1User
	err = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var u v1User
			if err := it.Item().Value(func(v []byte) error { return json.Unmarshal(v, &u) }); err != nil {
				stats.Invalid++
				continue
			}
			if u.Status != v1StatusPaid {
				stats.Pending++
				continue
			}
			if u.PubKey == "" {
				u.PubKey = string(it.Item().Key())
			}
			paid = append(paid, u)
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	for _, u := range paid {
		pk, err := pubkey.Parse(u.PubKey)
		if err != nil {
			stats.Invalid++
			log.Warn("skipping v1 user with invalid key", "key", u.PubKey)
			continue
		}
		note := "migrated from v1"
		if u.TxHash != "" {
			note += " (tx " + u.TxHash + ")"
		}
		if err := st.AddMember(ctx, pk, store.SourceImport, note); err != nil {
			return stats, err
		}
		stats.Imported++
	}
	return stats, nil
}

// openV1 opens the v1 database read-only. A v1 process that was killed leaves a
// memtable that Badger can only recover in read-write mode, so in that case the
// database is copied to scratchDir and recovered there.
func openV1(dir, scratchDir string, log *slog.Logger) (*badger.DB, func(), error) {
	if _, err := os.Stat(filepath.Join(dir, "MANIFEST")); err != nil {
		return nil, nil, fmt.Errorf("%s does not look like a nerostr v1 database: %w", dir, err)
	}
	db, err := badger.Open(badger.DefaultOptions(dir).WithReadOnly(true).WithLogger(nil))
	if err == nil {
		return db, func() { db.Close() }, nil
	}
	if !strings.Contains(err.Error(), "truncate required") {
		return nil, nil, fmt.Errorf("opening v1 database (is nerostr v1 still running?): %w", err)
	}

	log.Info("v1 database was not shut down cleanly; recovering a temporary copy")
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp(scratchDir, "v1-recovery-")
	if err != nil {
		return nil, nil, err
	}
	remove := func() { os.RemoveAll(tmp) }
	if err := copyDir(dir, tmp); err != nil {
		remove()
		return nil, nil, fmt.Errorf("copying v1 database: %w", err)
	}
	db, err = badger.Open(badger.DefaultOptions(tmp).WithLogger(nil))
	if err != nil {
		remove()
		return nil, nil, fmt.Errorf("recovering v1 database copy: %w", err)
	}
	return db, func() { db.Close(); remove() }, nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == "LOCK" {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
