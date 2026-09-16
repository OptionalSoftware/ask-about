package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// schema creates every table up front, including the two that stay empty until
// invite gating exists. Creating them now costs nothing and means adding
// invites later is a feature, not a migration.
//
// Times are stored as RFC3339 strings in UTC. SQLite has no date type, and
// text sorts correctly in that format, which keeps range queries honest.
const schema = `
-- A contact is whoever the link was made for: a person, a company, a job
-- posting. Free text, because the useful identifier depends on how they
-- reached you — an email from a recruiter, a name from LinkedIn, a company
-- and role from an application form.
--
-- name_key is the name lowercased and space-collapsed, unique so a second
-- link for the same contact attaches to the existing row instead of forking
-- on capitalisation. name keeps what was actually typed, for display.
CREATE TABLE IF NOT EXISTS contacts (
    id         TEXT PRIMARY KEY,
    name_key   TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- One row per link. Normally a contact has exactly one and keeps using it;
-- a second is issued when the first is lost or used up, and both stay under
-- the same contact.
--
-- note is private. The name is visible in the URL, this never is.
--
-- token is the plaintext half of the link, kept so it can be looked up and
-- sent again. It was digest-only at first, on the reasoning that a stolen
-- database would then hand over no working links — but that database already
-- holds every question and answer, so anyone with it has the content already
-- and has root besides. The trade was a small reduction in blast radius for
-- never being able to re-send a link, which is the wrong way round.
--
-- token_hash stays: lookups are by digest, so the column is indexed and unique
-- and the plaintext is only ever read for display.
CREATE TABLE IF NOT EXISTS invites (
    id         TEXT PRIMARY KEY,
    contact_id TEXT NOT NULL REFERENCES contacts(id),
    token_hash TEXT NOT NULL UNIQUE,
    token      TEXT NOT NULL DEFAULT '',
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS invites_contact ON invites(contact_id);

-- session_id comes from a cookie set on arrival and dropped when the browser
-- closes. It marks a visit: a real boundary the visitor draws by leaving,
-- rather than one guessed from gaps between timestamps.
CREATE TABLE IF NOT EXISTS turns (
    id                 TEXT PRIMARY KEY,
    invite_id          TEXT REFERENCES invites(id),
    session_id         TEXT NOT NULL DEFAULT '',
    vendor             TEXT NOT NULL DEFAULT '',
    model              TEXT NOT NULL DEFAULT '',
    question           TEXT NOT NULL,
    answer             TEXT NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    latency_ms         INTEGER NOT NULL DEFAULT 0,
    blocked            INTEGER NOT NULL DEFAULT 0,
    err                TEXT NOT NULL DEFAULT '',
    asked_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS turns_asked_at ON turns(asked_at);
CREATE INDEX IF NOT EXISTS turns_invite   ON turns(invite_id);
CREATE INDEX IF NOT EXISTS turns_session  ON turns(invite_id, session_id, asked_at);
`

type sqliteStore struct {
	db *sql.DB
}

// OpenSQLite opens (and creates, if absent) the database at path.
func OpenSQLite(path string) (Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}

	// WAL lets the admin read while a conversation is being written; busy_timeout
	// makes concurrent writers wait rather than immediately returning "database
	// is locked"; foreign_keys is off by default in SQLite and has to be asked
	// for or the references above are decoration.
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	if err := addMissingColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) RecordTurn(ctx context.Context, t *Turn) error {
	if t.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		t.ID = id
	}
	if t.AskedAt.IsZero() {
		t.AskedAt = time.Now()
	}

	var inviteID any // NULL rather than "" so the foreign key stays satisfiable
	if t.InviteID != "" {
		inviteID = t.InviteID
	}

	_, err := s.db.ExecContext(ctx, `
        INSERT INTO turns (
            id, invite_id, session_id, vendor, model, question, answer,
            input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
            latency_ms, blocked, err, asked_at
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, inviteID, t.SessionID, t.Vendor, t.Usage.Model, t.Question, t.Answer,
		t.Usage.InputTokens, t.Usage.CacheReadTokens,
		t.Usage.CacheWriteTokens, t.Usage.OutputTokens,
		t.Latency.Milliseconds(), t.Blocked, t.Err, utc(t.AskedAt),
	)
	if err != nil {
		return fmt.Errorf("store: record turn: %w", err)
	}
	return nil
}

// PruneTurns is the retention window. Turns are the only thing that holds
// what a visitor typed, so they are the only thing that expires.
func (s *sqliteStore) PruneTurns(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM turns WHERE asked_at < ?`, utc(before))
	if err != nil {
		return 0, fmt.Errorf("store: prune turns: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// addMissingColumns brings an existing database up to the schema above.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a
// column added later has to be applied separately. Rows written before it
// simply carry the zero value — an invite created when only the digest was
// stored keeps an empty token, and the admin pages say so rather than showing
// a broken link.
func addMissingColumns(db *sql.DB) error {
	for _, c := range []struct{ table, column, def string }{
		{"invites", "token", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := hasColumn(db, c.table, c.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.def)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("store: add %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("store: inspect %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("store: inspect %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
