package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// nameKey normalizes a contact name for matching: lowercased, with runs of
// whitespace collapsed. "Acme  Corp" and "acme corp" are the same contact;
// "Acme" and "Acme Corp" are not, which is why the create form offers the
// names already in use rather than relying on this to be clever.
func nameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// inviteUsage aggregates turns per link. A LEFT JOIN against a grouped
// subquery rather than a correlated subquery per column, so the turns table is
// scanned once regardless of how many links exist. COALESCE covers links that
// have never been used, whose row on the right of the join is entirely NULL.
const inviteUsage = `
    LEFT JOIN (
        SELECT invite_id,
               COUNT(*)                            AS turns,
               COUNT(DISTINCT NULLIF(session_id,'')) AS sessions,
               SUM(input_tokens)                   AS input_tokens,
               SUM(cache_read_tokens)              AS cache_read_tokens,
               SUM(cache_write_tokens)             AS cache_write_tokens,
               SUM(output_tokens)                  AS output_tokens,
               MAX(asked_at)                       AS last_seen,
               -- The model of the most recent turn. Without it the caller has
               -- nothing to look a rate up by and every cost reads "no rates".
               --
               -- One model per link rather than a breakdown: a link that spans
               -- a model change is priced entirely at the newer rate, which is
               -- approximate. The spend strip and the caps group by model
               -- properly, so the numbers that gate spending stay exact and it
               -- is only this per-link display that rounds.
               (SELECT t2.model FROM turns t2
                 WHERE t2.invite_id = turns.invite_id
                 ORDER BY t2.asked_at DESC LIMIT 1)  AS model
        FROM turns
        WHERE invite_id IS NOT NULL
        GROUP BY invite_id
    ) u ON u.invite_id = i.id`

const inviteColumns = `
    i.id, i.contact_id, i.token_hash, i.token, i.note,
    i.created_at, i.expires_at, i.revoked_at,
    c.name,
    COALESCE(u.turns, 0), COALESCE(u.sessions, 0),
    COALESCE(u.input_tokens, 0), COALESCE(u.cache_read_tokens, 0),
    COALESCE(u.cache_write_tokens, 0), COALESCE(u.output_tokens, 0),
    COALESCE(u.last_seen, ''), COALESCE(u.model, '')`

func (s *sqliteStore) CreateInvite(ctx context.Context, name, note, token, tokenHash string, expires *time.Time) (*Invite, error) {
	// Collapsed rather than merely trimmed, so the stored name matches what
	// matching sees. Otherwise "Acme   Corp" attaches to the right contact but
	// displays with the gap it was typed with.
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return nil, errors.New("store: invite requires a contact name")
	}
	if tokenHash == "" {
		return nil, errors.New("store: invite requires a token hash")
	}

	// The contact lookup and both inserts run as one transaction: without it,
	// two links created at the same moment for a new name could each insert
	// the contact, and the UNIQUE constraint would fail the wrong one.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: create invite: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	now := time.Now()

	var contactID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM contacts WHERE name_key = ?`, nameKey(name)).Scan(&contactID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if contactID, err = newID(); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO contacts (id, name_key, name, created_at) VALUES (?,?,?,?)`,
			contactID, nameKey(name), name, utc(now)); err != nil {
			return nil, fmt.Errorf("store: create contact: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("store: find contact: %w", err)
	}

	inviteID, err := newID()
	if err != nil {
		return nil, err
	}
	inv := &Invite{
		ID:        inviteID,
		ContactID: contactID,
		TokenHash: tokenHash,
		Token:     token,
		Note:      strings.TrimSpace(note),
		CreatedAt: now,
		ExpiresAt: expires,
	}
	if _, err = tx.ExecContext(ctx, `
        INSERT INTO invites (id, contact_id, token_hash, token, note, created_at, expires_at)
        VALUES (?,?,?,?,?,?,?)`,
		inv.ID, inv.ContactID, inv.TokenHash, inv.Token, inv.Note,
		utc(inv.CreatedAt), utcPtr(expires)); err != nil {
		return nil, fmt.Errorf("store: create invite: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: create invite: %w", err)
	}
	return inv, nil
}

func (s *sqliteStore) InviteByTokenHash(ctx context.Context, tokenHash string) (*InviteDetail, error) {
	if tokenHash == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT `+inviteColumns+`
        FROM invites i
        JOIN contacts c ON c.id = i.contact_id`+inviteUsage+`
        WHERE i.token_hash = ?`, tokenHash)

	d, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		// Not an error: an unknown token is a refusal, not a failure.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: invite by token: %w", err)
	}
	return d, nil
}

func (s *sqliteStore) ListContacts(ctx context.Context) ([]ContactDetail, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT `+inviteColumns+`, c.id, c.created_at
        FROM invites i
        JOIN contacts c ON c.id = i.contact_id`+inviteUsage+`
        ORDER BY c.created_at DESC, i.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list contacts: %w", err)
	}
	defer rows.Close()

	// One row per link, folded into one entry per contact. Grouping here
	// rather than in SQL keeps the totals in one place instead of duplicating
	// the aggregation at a second level.
	var out []ContactDetail
	byID := map[string]int{}
	for rows.Next() {
		var d InviteDetail
		var contactID, contactCreated string
		if err := scanInviteInto(rows, &d, &contactID, &contactCreated); err != nil {
			return nil, fmt.Errorf("store: scan contact: %w", err)
		}

		idx, ok := byID[contactID]
		if !ok {
			created, _ := time.Parse(time.RFC3339Nano, contactCreated)
			out = append(out, ContactDetail{
				Contact: Contact{ID: contactID, Name: d.Name, CreatedAt: created},
			})
			idx = len(out) - 1
			byID[contactID] = idx
		}

		c := &out[idx]
		c.Invites = append(c.Invites, d)
		c.Turns += d.Turns
		c.Sessions += d.Sessions
		c.Usage.InputTokens += d.Usage.InputTokens
		c.Usage.CacheReadTokens += d.Usage.CacheReadTokens
		c.Usage.CacheWriteTokens += d.Usage.CacheWriteTokens
		c.Usage.OutputTokens += d.Usage.OutputTokens
		if d.LastSeen.After(c.LastSeen) {
			c.LastSeen = d.LastSeen
		}
		if c.Usage.Model == "" {
			c.Usage.Model = d.Usage.Model
		}
	}
	return out, rows.Err()
}

func (s *sqliteStore) ContactNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM contacts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: contact names: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: scan contact name: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *sqliteStore) SessionsForInvite(ctx context.Context, inviteID string) ([]Session, error) {
	if inviteID == "" {
		return nil, nil
	}
	// Ordered by session then time so the fold below is a single pass. Turns
	// from a visitor with cookies off carry no session id; each becomes its
	// own visit rather than collapsing into one enormous fake session.
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, session_id, question, answer, model,
               input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
               latency_ms, err, asked_at
        FROM turns
        WHERE invite_id = ?
        ORDER BY session_id, asked_at`, inviteID)
	if err != nil {
		return nil, fmt.Errorf("store: sessions for invite: %w", err)
	}
	defer rows.Close()

	var out []Session
	byID := map[string]int{}
	for rows.Next() {
		var t Turn
		var ms int64
		var asked string
		if err := rows.Scan(
			&t.ID, &t.SessionID, &t.Question, &t.Answer, &t.Usage.Model,
			&t.Usage.InputTokens, &t.Usage.CacheReadTokens,
			&t.Usage.CacheWriteTokens, &t.Usage.OutputTokens,
			&ms, &t.Err, &asked,
		); err != nil {
			return nil, fmt.Errorf("store: scan turn: %w", err)
		}
		t.InviteID = inviteID
		t.Latency = time.Duration(ms) * time.Millisecond
		t.AskedAt, _ = time.Parse(time.RFC3339Nano, asked)

		key := t.SessionID
		if key == "" {
			key = "turn:" + t.ID
		}
		idx, ok := byID[key]
		if !ok {
			out = append(out, Session{ID: key, Started: t.AskedAt})
			idx = len(out) - 1
			byID[key] = idx
		}

		sess := &out[idx]
		sess.Turns = append(sess.Turns, t)
		sess.Ended = t.AskedAt
		sess.Usage.InputTokens += t.Usage.InputTokens
		sess.Usage.CacheReadTokens += t.Usage.CacheReadTokens
		sess.Usage.CacheWriteTokens += t.Usage.CacheWriteTokens
		sess.Usage.OutputTokens += t.Usage.OutputTokens
		if sess.Usage.Model == "" {
			sess.Usage.Model = t.Usage.Model
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Newest visit first. Sorted here because the query ordered by session id
	// to make the fold above a single pass, which says nothing about time.
	slices.SortFunc(out, func(a, b Session) int { return b.Started.Compare(a.Started) })
	return out, nil
}

func (s *sqliteStore) UpdateInvite(ctx context.Context, id, note string, expires *time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invites SET note = ?, expires_at = ? WHERE id = ?`,
		strings.TrimSpace(note), utcPtr(expires), id)
	if err != nil {
		return fmt.Errorf("store: update invite: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: update invite %s: not found", id)
	}
	return nil
}

// TurnsForSession returns the last limit completed turns of one visit, oldest
// first — the order a conversation is replayed in.
//
// Failed turns are left out. Their answer is empty, and an empty assistant
// message is not something to hand back to a model as context.
func (s *sqliteStore) TurnsForSession(ctx context.Context, sessionID string, limit int) ([]Turn, error) {
	if sessionID == "" || limit <= 0 {
		return nil, nil
	}
	// Newest first so LIMIT keeps the most recent turns rather than the
	// oldest; reversed below into conversation order.
	rows, err := s.db.QueryContext(ctx, `
        SELECT question, answer, asked_at
        FROM turns
        WHERE session_id = ? AND err = '' AND answer <> ''
        ORDER BY asked_at DESC
        LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: turns for session: %w", err)
	}
	defer rows.Close()

	var out []Turn
	for rows.Next() {
		t := Turn{SessionID: sessionID}
		var asked string
		if err := rows.Scan(&t.Question, &t.Answer, &asked); err != nil {
			return nil, fmt.Errorf("store: scan turn: %w", err)
		}
		t.AskedAt, _ = time.Parse(time.RFC3339Nano, asked)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

func (s *sqliteStore) RevokeInvite(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		utc(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: revoke invite: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: revoke invite %s: not found or already revoked", id)
	}
	return nil
}

// scanner covers both *sql.Row and *sql.Rows so the queries above share one
// column ordering, which is the part that silently rots when duplicated.
type scanner interface{ Scan(dest ...any) error }

func scanInvite(sc scanner) (*InviteDetail, error) {
	var d InviteDetail
	if err := scanInviteInto(sc, &d, nil, nil); err != nil {
		return nil, err
	}
	return &d, nil
}

// scanInviteInto reads inviteColumns, optionally followed by the two extra
// contact columns the list query appends.
func scanInviteInto(sc scanner, d *InviteDetail, contactID, contactCreated *string) error {
	var created string
	var expires, revoked sql.NullString
	var lastSeen string

	dest := []any{
		&d.ID, &d.ContactID, &d.TokenHash, &d.Token, &d.Note,
		&created, &expires, &revoked,
		&d.Name,
		&d.Turns, &d.Sessions,
		&d.Usage.InputTokens, &d.Usage.CacheReadTokens,
		&d.Usage.CacheWriteTokens, &d.Usage.OutputTokens,
		&lastSeen, &d.Usage.Model,
	}
	if contactID != nil {
		dest = append(dest, contactID, contactCreated)
	}
	if err := sc.Scan(dest...); err != nil {
		return err
	}

	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	d.ExpiresAt = parseNullTime(expires)
	d.RevokedAt = parseNullTime(revoked)
	if lastSeen != "" {
		d.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
	}
	return nil
}

func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func utcPtr(t *time.Time) any {
	if t == nil {
		return nil // NULL, meaning no expiry
	}
	return utc(*t)
}

// usageGroupSQL sums tokens per model. Grouped rather than totalled because a
// day can span a model change, and the four token kinds price differently per
// model — one blended total could not be priced correctly afterwards.
const usageGroupSQL = `
    SELECT model,
           COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
           COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(output_tokens), 0)
    FROM turns
    WHERE %s
    GROUP BY model`

func (s *sqliteStore) scanUsage(rows *sql.Rows, err error, what string) ([]Usage, error) {
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()

	var out []Usage
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.Model, &u.InputTokens, &u.CacheReadTokens,
			&u.CacheWriteTokens, &u.OutputTokens); err != nil {
			return nil, fmt.Errorf("store: scan %s: %w", what, err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SessionUsage totals one visit, keyed on the session alone so it works on an
// open site too, where there is no invite to key on.
func (s *sqliteStore) SessionUsage(ctx context.Context, sessionID string) ([]Usage, error) {
	if sessionID == "" {
		// A visitor with cookies off has no visit to total. Refusing to answer
		// would be worse than not capping them; the daily cap still applies.
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(usageGroupSQL, "session_id = ?"), sessionID)
	return s.scanUsage(rows, err, "session usage")
}

// UsageSince totals everything from a moment onward, across every visitor.
func (s *sqliteStore) UsageSince(ctx context.Context, since time.Time) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(usageGroupSQL, "asked_at >= ?"), utc(since))
	return s.scanUsage(rows, err, "usage since")
}

// UsageBetween totals a half-open window [from, to).
//
// Days are bounded by the caller rather than grouped in SQL, because the
// boundary that matters is local midnight and the timestamps are stored in
// UTC. Grouping on the stored string would bucket by UTC date, putting the
// admin view and the daily cap on different definitions of "today" — and it
// would get the hour wrong twice a year besides.
func (s *sqliteStore) UsageBetween(ctx context.Context, from, to time.Time) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(usageGroupSQL, "asked_at >= ? AND asked_at < ?"), utc(from), utc(to))
	return s.scanUsage(rows, err, "usage between")
}

// SearchTurns finds questions and answers containing term.
//
// LIKE rather than a full-text index: at a few thousand turns a scan is
// instant, and FTS5 would mean a second table to keep in step with this one
// for ranking nobody is going to read. Revisit if the corpus of questions ever
// gets large enough to notice.
func (s *sqliteStore) SearchTurns(ctx context.Context, term string, limit int) ([]TurnMatch, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// Escaped so a literal % or _ in the term matches itself rather than
	// acting as a wildcard.
	pattern := "%" + likeEscape(term) + "%"

	rows, err := s.db.QueryContext(ctx, `
        SELECT t.id, COALESCE(t.invite_id,''), t.session_id, t.question, t.answer,
               t.model, t.output_tokens, t.err, t.asked_at,
               COALESCE(c.name, '')
        FROM turns t
        LEFT JOIN invites  i ON i.id = t.invite_id
        LEFT JOIN contacts c ON c.id = i.contact_id
        WHERE t.question LIKE ? ESCAPE '\' OR t.answer LIKE ? ESCAPE '\'
        ORDER BY t.asked_at DESC
        LIMIT ?`, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search turns: %w", err)
	}
	defer rows.Close()

	var out []TurnMatch
	for rows.Next() {
		var m TurnMatch
		var asked string
		if err := rows.Scan(&m.ID, &m.InviteID, &m.SessionID, &m.Question, &m.Answer,
			&m.Usage.Model, &m.Usage.OutputTokens, &m.Err, &asked, &m.ContactName); err != nil {
			return nil, fmt.Errorf("store: scan match: %w", err)
		}
		m.AskedAt, _ = time.Parse(time.RFC3339Nano, asked)
		out = append(out, m)
	}
	return out, rows.Err()
}

// likeEscape neutralises LIKE's wildcards so a search for "50%" finds "50%".
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}
