package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	_ "modernc.org/sqlite"

	"github.com/pluja/nerostr/internal/xmr"
)

// Member sources.
const (
	SourcePayment = "payment"
	SourceAdmin   = "admin"
	SourceImport  = "import"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

type Member struct {
	PubKey    nostr.PubKey
	Source    string
	Note      string
	CreatedAt time.Time
}

type Entry struct {
	Key       string
	Reason    string
	CreatedAt time.Time
}

type Invoice struct {
	ID           int64
	PubKey       nostr.PubKey
	Address      string
	SubaddrIndex uint32
	Amount       xmr.Piconero
	WalletHeight uint64
	CreatedAt    time.Time
	ExpiresAt    time.Time
	// Seen includes unconfirmed transfers; Confirmed only counts transfers with enough confirmations.
	Seen      xmr.Piconero
	Confirmed xmr.Piconero
	PaidAt    *time.Time
}

func (i *Invoice) Paid() bool { return i.PaidAt != nil }

func (i *Invoice) Expired(now time.Time) bool { return !i.Paid() && now.After(i.ExpiresAt) }

var migrations = []string{
	`CREATE TABLE members (
		pubkey     TEXT PRIMARY KEY,
		source     TEXT NOT NULL,
		note       TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	) STRICT;
	CREATE TABLE banned_pubkeys (
		pubkey     TEXT PRIMARY KEY,
		reason     TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	) STRICT;
	CREATE TABLE banned_events (
		id         TEXT PRIMARY KEY,
		reason     TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	) STRICT;
	CREATE TABLE blocked_ips (
		ip         TEXT PRIMARY KEY,
		reason     TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	) STRICT;
	CREATE TABLE invoices (
		id            INTEGER PRIMARY KEY,
		pubkey        TEXT NOT NULL,
		address       TEXT NOT NULL UNIQUE,
		subaddr_index INTEGER NOT NULL UNIQUE,
		amount        INTEGER NOT NULL,
		wallet_height INTEGER NOT NULL,
		created_at    INTEGER NOT NULL,
		expires_at    INTEGER NOT NULL,
		seen          INTEGER NOT NULL DEFAULT 0,
		confirmed     INTEGER NOT NULL DEFAULT 0,
		paid_at       INTEGER
	) STRICT;
	CREATE INDEX invoices_pubkey ON invoices (pubkey, created_at);
	CREATE INDEX invoices_unpaid ON invoices (created_at) WHERE paid_at IS NULL;`,
}

func Open(path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(10000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA cannot take bound parameters; the value is the migration index, not input.
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent snapshot of the database to path.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

func now() int64 { return time.Now().Unix() }

func exists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// --- members ---

func (s *Store) IsMember(ctx context.Context, pk nostr.PubKey) (bool, error) {
	return exists(ctx, s.db, `SELECT 1 FROM members WHERE pubkey = ?`, pk.Hex())
}

// AddMember grants write access. It is a no-op for existing members and lifts any ban.
func (s *Store) AddMember(ctx context.Context, pk nostr.PubKey, source, note string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM banned_pubkeys WHERE pubkey = ?`, pk.Hex()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO members (pubkey, source, note, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (pubkey) DO NOTHING`,
		pk.Hex(), source, note, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveMember(ctx context.Context, pk nostr.PubKey) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM members WHERE pubkey = ?`, pk.Hex())
	return err
}

func (s *Store) GetMember(ctx context.Context, pk nostr.PubKey) (Member, error) {
	m := Member{PubKey: pk}
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT source, note, created_at FROM members WHERE pubkey = ?`, pk.Hex()).
		Scan(&m.Source, &m.Note, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.CreatedAt = time.Unix(created, 0)
	return m, err
}

func (s *Store) ListMembers(ctx context.Context) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pubkey, source, note, created_at FROM members ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var hex string
		var m Member
		var created int64
		if err := rows.Scan(&hex, &m.Source, &m.Note, &created); err != nil {
			return nil, err
		}
		m.PubKey, err = nostr.PubKeyFromHexCheap(hex)
		if err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(created, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CountMembers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM members`).Scan(&n)
	return n, err
}

// --- bans and blocks ---

func (s *Store) IsBannedPubKey(ctx context.Context, pk nostr.PubKey) (bool, error) {
	return exists(ctx, s.db, `SELECT 1 FROM banned_pubkeys WHERE pubkey = ?`, pk.Hex())
}

// BanPubKey revokes membership and blocks the key from publishing.
func (s *Store) BanPubKey(ctx context.Context, pk nostr.PubKey, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM members WHERE pubkey = ?`, pk.Hex()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO banned_pubkeys (pubkey, reason, created_at) VALUES (?, ?, ?) ON CONFLICT (pubkey) DO UPDATE SET reason = excluded.reason`,
		pk.Hex(), reason, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnbanPubKey(ctx context.Context, pk nostr.PubKey) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM banned_pubkeys WHERE pubkey = ?`, pk.Hex())
	return err
}

func (s *Store) ListBannedPubKeys(ctx context.Context) ([]Entry, error) {
	return s.list(ctx, `SELECT pubkey, reason, created_at FROM banned_pubkeys ORDER BY created_at`)
}

func (s *Store) IsBannedEvent(ctx context.Context, id nostr.ID) (bool, error) {
	return exists(ctx, s.db, `SELECT 1 FROM banned_events WHERE id = ?`, id.Hex())
}

func (s *Store) BanEvent(ctx context.Context, id nostr.ID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO banned_events (id, reason, created_at) VALUES (?, ?, ?) ON CONFLICT (id) DO UPDATE SET reason = excluded.reason`,
		id.Hex(), reason, now())
	return err
}

func (s *Store) UnbanEvent(ctx context.Context, id nostr.ID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM banned_events WHERE id = ?`, id.Hex())
	return err
}

func (s *Store) ListBannedEvents(ctx context.Context) ([]Entry, error) {
	return s.list(ctx, `SELECT id, reason, created_at FROM banned_events ORDER BY created_at`)
}

func (s *Store) IsBlockedIP(ctx context.Context, ip string) (bool, error) {
	return exists(ctx, s.db, `SELECT 1 FROM blocked_ips WHERE ip = ?`, ip)
}

func (s *Store) BlockIP(ctx context.Context, ip, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO blocked_ips (ip, reason, created_at) VALUES (?, ?, ?) ON CONFLICT (ip) DO UPDATE SET reason = excluded.reason`,
		ip, reason, now())
	return err
}

func (s *Store) UnblockIP(ctx context.Context, ip string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM blocked_ips WHERE ip = ?`, ip)
	return err
}

func (s *Store) ListBlockedIPs(ctx context.Context) ([]Entry, error) {
	return s.list(ctx, `SELECT ip, reason, created_at FROM blocked_ips ORDER BY created_at`)
}

func (s *Store) list(ctx context.Context, query string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var created int64
		if err := rows.Scan(&e.Key, &e.Reason, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- invoices ---

const invoiceColumns = `id, pubkey, address, subaddr_index, amount, wallet_height, created_at, expires_at, seen, confirmed, paid_at`

func scanInvoice(row interface{ Scan(...any) error }) (Invoice, error) {
	var inv Invoice
	var hex string
	var created, expires int64
	var amount, seen, confirmed int64
	var paid sql.NullInt64
	if err := row.Scan(&inv.ID, &hex, &inv.Address, &inv.SubaddrIndex, &amount, &inv.WalletHeight,
		&created, &expires, &seen, &confirmed, &paid); err != nil {
		return inv, err
	}
	pk, err := nostr.PubKeyFromHexCheap(hex)
	if err != nil {
		return inv, err
	}
	inv.PubKey = pk
	inv.Amount, inv.Seen, inv.Confirmed = fromDB(amount), fromDB(seen), fromDB(confirmed)
	inv.CreatedAt, inv.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
	if paid.Valid {
		t := time.Unix(paid.Int64, 0)
		inv.PaidAt = &t
	}
	return inv, nil
}

func (s *Store) CreateInvoice(ctx context.Context, inv *Invoice) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO invoices (pubkey, address, subaddr_index, amount, wallet_height, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.PubKey.Hex(), inv.Address, inv.SubaddrIndex, toDB(inv.Amount), inv.WalletHeight,
		inv.CreatedAt.Unix(), inv.ExpiresAt.Unix())
	if err != nil {
		return err
	}
	inv.ID, err = res.LastInsertId()
	return err
}

// LatestInvoice returns the most recent invoice for a pubkey.
func (s *Store) LatestInvoice(ctx context.Context, pk nostr.PubKey) (Invoice, error) {
	inv, err := scanInvoice(s.db.QueryRowContext(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE pubkey = ? ORDER BY created_at DESC, id DESC LIMIT 1`, pk.Hex()))
	if errors.Is(err, sql.ErrNoRows) {
		return inv, ErrNotFound
	}
	return inv, err
}

// CountOpenInvoices counts unpaid invoices that have not expired yet.
func (s *Store) CountOpenInvoices(ctx context.Context, at time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM invoices WHERE paid_at IS NULL AND expires_at > ?`, at.Unix()).Scan(&n)
	return n, err
}

// WatchedInvoices returns unpaid invoices created after since, i.e. those whose
// payments should still be credited.
func (s *Store) WatchedInvoices(ctx context.Context, since time.Time) ([]Invoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+invoiceColumns+` FROM invoices WHERE paid_at IS NULL AND created_at >= ? ORDER BY id`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInvoiceTotals(ctx context.Context, id int64, seen, confirmed xmr.Piconero) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE invoices SET seen = ?, confirmed = ? WHERE id = ? AND paid_at IS NULL`,
		toDB(seen), toDB(confirmed), id)
	return err
}

// MarkPaid records the payment and grants membership atomically. A banned key
// keeps its ban: paying does not override an operator decision.
func (s *Store) MarkPaid(ctx context.Context, id int64, seen, confirmed xmr.Piconero, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hex string
	err = tx.QueryRowContext(ctx,
		`UPDATE invoices SET seen = ?, confirmed = ?, paid_at = ? WHERE id = ? AND paid_at IS NULL RETURNING pubkey`,
		toDB(seen), toDB(confirmed), at.Unix(), id).Scan(&hex)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	note := "invoice " + strconv.FormatInt(id, 10)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO members (pubkey, source, note, created_at)
		 SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM banned_pubkeys WHERE pubkey = ?)
		 ON CONFLICT (pubkey) DO NOTHING`,
		hex, SourcePayment, note, at.Unix(), hex); err != nil {
		return err
	}
	return tx.Commit()
}

// toDB and fromDB convert amounts for SQLite's signed integers. Saturating is
// safe: reaching the limit would take half of all XMR in existence.
func toDB(p xmr.Piconero) int64 {
	if p > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(p)
}

func fromDB(v int64) xmr.Piconero {
	if v < 0 {
		return 0
	}
	return xmr.Piconero(v)
}
