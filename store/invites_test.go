package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) Store {
	t.Helper()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A replacement link — issued when the first is lost or used up — must land
// under the same contact, or the history splits into two strangers.
func TestCreateInviteReusesContact(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	first, err := db.CreateInvite(ctx, "Acme Corp", "staff eng", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("first link: %v", err)
	}
	// Different capitalisation and spacing, same contact.
	second, err := db.CreateInvite(ctx, "  acme   corp ", "replacement", "tok2", "hash-2", nil)
	if err != nil {
		t.Fatalf("second link: %v", err)
	}

	if first.ContactID != second.ContactID {
		t.Errorf("same name produced two contacts: %s and %s", first.ContactID, second.ContactID)
	}
	if first.ID == second.ID {
		t.Error("two links share an id")
	}

	contacts, err := db.ListContacts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(contacts))
	}
	if len(contacts[0].Invites) != 2 {
		t.Errorf("got %d links under the contact, want 2", len(contacts[0].Invites))
	}
	// The display name keeps what was typed first, not the messy second form.
	if contacts[0].Name != "Acme Corp" {
		t.Errorf("name = %q, want the original spelling", contacts[0].Name)
	}
}

// A different name is a different contact — the form's suggestions are what
// stop that happening by accident, not the matching here.
func TestCreateInviteSeparatesContacts(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	if _, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.CreateInvite(ctx, "Acme", "", "tok2", "hash-2", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	contacts, _ := db.ListContacts(ctx)
	if len(contacts) != 2 {
		t.Errorf("got %d contacts, want 2", len(contacts))
	}

	names, err := db.ContactNames(ctx)
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	if len(names) != 2 || names[0] != "Acme" || names[1] != "Acme Corp" {
		t.Errorf("names = %v, want both sorted", names)
	}
}

func TestInviteByTokenHash(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.InviteByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || got.ID != inv.ID {
		t.Fatalf("lookup returned %+v, want invite %s", got, inv.ID)
	}

	// An unknown token is an ordinary refusal, not an error to log.
	missing, err := db.InviteByTokenHash(ctx, "hash-nope")
	if err != nil {
		t.Errorf("unknown token returned an error: %v", err)
	}
	if missing != nil {
		t.Error("unknown token resolved to an invite")
	}
}

// Usage is aggregated per link so the admin pages can show what each one has
// cost without the caller walking every turn.
func TestListContactsAggregatesUsage(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A link that is never used must report zeroes, not NULLs.
	if _, err := db.CreateInvite(ctx, "Northwind", "", "tok2", "hash-2", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	base := time.Now().Add(-time.Hour)
	for i, out := range []int64{100, 250} {
		err := db.RecordTurn(ctx, &Turn{
			InviteID:  inv.ID,
			SessionID: "visit-1",
			Question:  "q",
			Answer:    "a",
			Usage:     Usage{InputTokens: 10, CacheReadTokens: 27000, OutputTokens: out},
			AskedAt:   base.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("record turn: %v", err)
		}
	}
	// A second visit on the same link, months later.
	if err := db.RecordTurn(ctx, &Turn{
		InviteID: inv.ID, SessionID: "visit-2", Question: "q", Answer: "a",
		Usage:   Usage{OutputTokens: 50},
		AskedAt: base.Add(90 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("record turn: %v", err)
	}

	contacts, err := db.ListContacts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := map[string]ContactDetail{}
	for _, c := range contacts {
		byName[c.Name] = c
	}

	used := byName["Acme Corp"]
	if used.Turns != 3 {
		t.Errorf("turns = %d, want 3", used.Turns)
	}
	if used.Sessions != 2 {
		t.Errorf("sessions = %d, want 2 distinct visits", used.Sessions)
	}
	if used.Usage.OutputTokens != 400 {
		t.Errorf("output tokens = %d, want 400", used.Usage.OutputTokens)
	}
	if used.Usage.CacheReadTokens != 54000 {
		t.Errorf("cache read tokens = %d, want 54000", used.Usage.CacheReadTokens)
	}
	if used.LastSeen.IsZero() {
		t.Error("last seen is zero for a used link")
	}

	unused := byName["Northwind"]
	if unused.Turns != 0 || unused.Sessions != 0 || unused.Usage.OutputTokens != 0 {
		t.Errorf("unused link reported usage: turns=%d sessions=%d %+v",
			unused.Turns, unused.Sessions, unused.Usage)
	}
	if !unused.LastSeen.IsZero() {
		t.Error("unused link has a last-seen time")
	}
}

// Visits are what the detail page shows: which days they came back, and what
// they asked each time.
func TestSessionsForInvite(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	march := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	june := time.Date(2026, 6, 13, 14, 0, 0, 0, time.UTC)
	for _, turn := range []struct {
		session string
		at      time.Time
		q       string
	}{
		{"visit-march", march, "first question"},
		{"visit-march", march.Add(4 * time.Minute), "second question"},
		{"visit-june", june, "back again"},
	} {
		if err := db.RecordTurn(ctx, &Turn{
			InviteID: inv.ID, SessionID: turn.session,
			Question: turn.q, Answer: "a",
			Usage:   Usage{OutputTokens: 10},
			AskedAt: turn.at,
		}); err != nil {
			t.Fatalf("record turn: %v", err)
		}
	}

	sessions, err := db.SessionsForInvite(ctx, inv.ID)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d visits, want 2", len(sessions))
	}

	// Newest first, so the most recent visit is the one you see.
	if !sessions[0].Started.Equal(june) {
		t.Errorf("first visit started %v, want the June one", sessions[0].Started)
	}
	if len(sessions[1].Turns) != 2 {
		t.Errorf("March visit has %d turns, want 2", len(sessions[1].Turns))
	}
	if sessions[1].Turns[0].Question != "first question" {
		t.Errorf("turns are out of order within a visit: %q", sessions[1].Turns[0].Question)
	}
	if !sessions[1].Ended.Equal(march.Add(4 * time.Minute)) {
		t.Errorf("visit ended %v, want the last turn's time", sessions[1].Ended)
	}
	if sessions[1].Usage.OutputTokens != 20 {
		t.Errorf("visit output tokens = %d, want 20", sessions[1].Usage.OutputTokens)
	}
}

// A visitor with cookies off sends no session id. Their turns must not all
// collapse into one enormous fake visit.
func TestSessionsWithoutCookie(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, _ := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	for i := range 3 {
		if err := db.RecordTurn(ctx, &Turn{
			InviteID: inv.ID, Question: "q", Answer: "a",
			AskedAt: time.Now().Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("record turn: %v", err)
		}
	}

	sessions, err := db.SessionsForInvite(ctx, inv.ID)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("got %d visits, want 3 — one per turn", len(sessions))
	}
}

func TestTurnsForSession(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	base := time.Now().Add(-time.Hour)
	record := func(session, q, a, errMsg string, offset time.Duration) {
		t.Helper()
		if err := db.RecordTurn(ctx, &Turn{
			SessionID: session, Question: q, Answer: a, Err: errMsg,
			AskedAt: base.Add(offset),
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	record("visit-1", "first", "answer one", "", 0)
	record("visit-1", "second", "answer two", "", time.Minute)
	record("visit-1", "failed", "", "upstream exploded", 2*time.Minute)
	record("visit-2", "someone else", "not yours", "", 3*time.Minute)

	got, err := db.TurnsForSession(ctx, "visit-1", 10)
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	// Oldest first — the order a conversation is replayed in — and the failed
	// turn left out, since an empty answer is not context.
	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Question != want[i] {
			t.Errorf("turn %d = %q, want %q", i, got[i].Question, want[i])
		}
	}

	// The limit keeps the most recent turns, not the first ones off the top.
	got, err = db.TurnsForSession(ctx, "visit-1", 1)
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if len(got) != 1 || got[0].Question != "second" {
		t.Errorf("limited to 1 = %+v, want the most recent turn", got)
	}

	// A visitor with cookies off has no visit, and so no history.
	if got, err := db.TurnsForSession(ctx, "", 10); err != nil || got != nil {
		t.Errorf("empty session = %+v, %v; want nil, nil", got, err)
	}
}

func TestUpdateInvite(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "first note", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	later := time.Now().AddDate(0, 0, 30)
	if err := db.UpdateInvite(ctx, inv.ID, "  second note  ", &later); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := db.InviteByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Note != "second note" {
		t.Errorf("note = %q, want %q", got.Note, "second note")
	}
	if got.ExpiresAt == nil {
		t.Fatal("expiry not set")
	}
	if d := got.ExpiresAt.Sub(later); d > time.Second || d < -time.Second {
		t.Errorf("expiry = %v, want %v", got.ExpiresAt, later)
	}

	// Both fields are written every time, so a nil expires clears one that was
	// set rather than leaving it in place.
	if err := db.UpdateInvite(ctx, inv.ID, "", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = db.InviteByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("lookup after clear: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("expiry = %v, want none", got.ExpiresAt)
	}
	if got.Note != "" {
		t.Errorf("note = %q, want empty", got.Note)
	}

	// The token and its digest are untouched, so the link still works.
	if got.Token != "tok1" || got.TokenHash != "hash-1" {
		t.Errorf("token = %q/%q, want tok1/hash-1", got.Token, got.TokenHash)
	}

	if err := db.UpdateInvite(ctx, "no-such-id", "note", nil); err == nil {
		t.Error("updating an unknown id succeeded, want an error")
	}
}

func TestRevokeInvite(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.RevokeInvite(ctx, inv.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	got, err := db.InviteByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// Revoking stamps rather than deletes, so the row and its turns survive.
	if got == nil {
		t.Fatal("revoked link disappeared")
	}
	if got.RevokedAt == nil {
		t.Error("revoked link has no revoked_at")
	}

	if err := db.RevokeInvite(ctx, inv.ID); err == nil {
		t.Error("revoking twice succeeded, want an error")
	}
	if err := db.RevokeInvite(ctx, "no-such-id"); err == nil {
		t.Error("revoking an unknown id succeeded, want an error")
	}
}

// Expiry is optional: the common case is a link retired by revoking it or by
// running out of budget, not by a calendar.
func TestCreateInviteWithoutExpiry(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	if _, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.InviteByTokenHash(ctx, "hash-1")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("expires at = %v, want nil", got.ExpiresAt)
	}

	when := time.Now().Add(72 * time.Hour)
	if _, err := db.CreateInvite(ctx, "Northwind", "", "tok2", "hash-2", &when); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ = db.InviteByTokenHash(ctx, "hash-2")
	if got.ExpiresAt == nil {
		t.Fatal("expiry was not stored")
	}
	if got.ExpiresAt.Sub(when).Abs() > time.Second {
		t.Errorf("expires at = %v, want %v", got.ExpiresAt, when)
	}
}

func TestCreateInviteValidates(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	if _, err := db.CreateInvite(ctx, "  ", "", "tok", "hash", nil); err == nil {
		t.Error("empty name was accepted")
	}
	if _, err := db.CreateInvite(ctx, "Acme Corp", "", "tok", "", nil); err == nil {
		t.Error("empty token hash was accepted")
	}
	if contacts, _ := db.ListContacts(ctx); len(contacts) != 0 {
		t.Errorf("rejected links left %d contacts behind", len(contacts))
	}
}

// Without the model on the aggregate there is nothing to look a rate up by,
// and every cost on the admin pages reads "no rates" while the daily strip
// prices fine — which looks like a pricing problem rather than a missing join.
func TestListContactsCarriesModel(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	inv, err := db.CreateInvite(ctx, "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.RecordTurn(ctx, &Turn{
		InviteID: inv.ID, SessionID: "v1", Question: "q", Answer: "a",
		Usage:   Usage{Model: "claude-sonnet-5", OutputTokens: 100},
		AskedAt: time.Now(),
	}); err != nil {
		t.Fatalf("record turn: %v", err)
	}

	contacts, err := db.ListContacts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := contacts[0].Invites[0].Usage.Model; got != "claude-sonnet-5" {
		t.Errorf("invite model = %q, want the recorded model", got)
	}
	if got := contacts[0].Usage.Model; got != "claude-sonnet-5" {
		t.Errorf("contact model = %q, want it rolled up", got)
	}
}

// A link with no turns has no model, and must not claim one.
func TestUnusedInviteHasNoModel(t *testing.T) {
	db := testStore(t)
	if _, err := db.CreateInvite(t.Context(), "Acme Corp", "", "tok1", "hash-1", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	contacts, _ := db.ListContacts(t.Context())
	if got := contacts[0].Invites[0].Usage.Model; got != "" {
		t.Errorf("unused link reported model %q", got)
	}
}

// UsageBetween is a half-open window, so a turn at the boundary lands in
// exactly one day rather than both or neither.
func TestUsageBetweenIsHalfOpen(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()

	midnight := time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local)
	for _, at := range []time.Time{
		midnight.Add(-time.Nanosecond), // yesterday
		midnight,                       // today, first instant
		midnight.Add(23 * time.Hour),   // today
	} {
		if err := db.RecordTurn(ctx, &Turn{
			Question: "q", Answer: "a",
			Usage:   Usage{Model: "m", OutputTokens: 10},
			AskedAt: at,
		}); err != nil {
			t.Fatalf("record turn: %v", err)
		}
	}

	today, err := db.UsageBetween(ctx, midnight, midnight.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("usage between: %v", err)
	}
	var sum int64
	for _, u := range today {
		sum += u.OutputTokens
	}
	if sum != 20 {
		t.Errorf("today's output tokens = %d, want 20 (the boundary turn counts once)", sum)
	}
}

// The link has to be recoverable, or a created invite can never be sent again.
func TestInviteKeepsItsToken(t *testing.T) {
	db := testStore(t)
	inv, err := db.CreateInvite(t.Context(), "Acme Corp", "", "4lactnl5ip6o4mtx", "hash-1", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inv.Token != "4lactnl5ip6o4mtx" {
		t.Errorf("returned token = %q", inv.Token)
	}

	contacts, _ := db.ListContacts(t.Context())
	if got := contacts[0].Invites[0].Token; got != "4lactnl5ip6o4mtx" {
		t.Errorf("stored token = %q, want it readable back", got)
	}
	// The digest is still what lookups use.
	found, _ := db.InviteByTokenHash(t.Context(), "hash-1")
	if found == nil || found.Token != "4lactnl5ip6o4mtx" {
		t.Errorf("lookup by digest lost the token: %+v", found)
	}
}

// A database written before the token column existed has to keep working. Its
// rows carry an empty token, which the admin pages report rather than
// rendering a broken URL.
func TestOpensADatabaseWithoutTheTokenColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Build the older shape by hand: same table, no token column.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = old.Exec(`
        CREATE TABLE contacts (id TEXT PRIMARY KEY, name_key TEXT NOT NULL UNIQUE,
            name TEXT NOT NULL, created_at TEXT NOT NULL);
        CREATE TABLE invites (id TEXT PRIMARY KEY, contact_id TEXT NOT NULL,
            token_hash TEXT NOT NULL UNIQUE, note TEXT NOT NULL DEFAULT '',
            created_at TEXT NOT NULL, expires_at TEXT, revoked_at TEXT);
        INSERT INTO contacts VALUES ('c1','acme corp','Acme Corp','2026-08-01T00:00:00Z');
        INSERT INTO invites (id, contact_id, token_hash, note, created_at)
            VALUES ('i1','c1','oldhash','staff eng','2026-08-01T00:00:00Z');`)
	if err != nil {
		t.Fatalf("build old schema: %v", err)
	}
	old.Close()

	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("opening a pre-token database failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	contacts, err := db.ListContacts(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(contacts) != 1 || len(contacts[0].Invites) != 1 {
		t.Fatalf("the existing row did not survive: %+v", contacts)
	}
	inv := contacts[0].Invites[0]
	if inv.Token != "" {
		t.Errorf("an old row gained a token from nowhere: %q", inv.Token)
	}
	if inv.Note != "staff eng" {
		t.Errorf("the migration lost data: note = %q", inv.Note)
	}
	// And a new invite in the migrated database keeps its token.
	if _, err := db.CreateInvite(t.Context(), "Northwind", "", "newtok", "newhash", nil); err != nil {
		t.Fatalf("create after migration: %v", err)
	}
	contacts, _ = db.ListContacts(t.Context())
	for _, c := range contacts {
		if c.Name == "Northwind" && c.Invites[0].Token != "newtok" {
			t.Errorf("new invite token = %q", c.Invites[0].Token)
		}
	}
}

func TestSearchTurns(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	inv, _ := db.CreateInvite(ctx, "Acme Corp", "", "tok", "hash-1", nil)

	for _, q := range []struct{ question, answer string }{
		{"What was their scope at Northwind?", "They led 40 people across billing and search."},
		{"Tell me about a turnaround", "A migration that had stalled for a year."},
		{"Does he know Kubernetes?", "Yes — containers and orchestration."},
	} {
		if err := db.RecordTurn(ctx, &Turn{
			InviteID: inv.ID, SessionID: "v1",
			Question: q.question, Answer: q.answer,
			Usage: Usage{Model: "m"}, AskedAt: time.Now(),
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	// Matches the question.
	got, err := db.SearchTurns(ctx, "turnaround", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].ContactName != "Acme Corp" {
		t.Errorf("question search returned %+v", got)
	}

	// Matches the answer too — often where the word you remember actually is.
	if got, _ = db.SearchTurns(ctx, "orchestration", 10); len(got) != 1 {
		t.Errorf("answer search returned %d, want 1", len(got))
	}

	// Case-insensitive, since nobody remembers how they typed it.
	if got, _ = db.SearchTurns(ctx, "NORTHWIND", 10); len(got) != 1 {
		t.Errorf("case-insensitive search returned %d, want 1", len(got))
	}

	if got, _ = db.SearchTurns(ctx, "quantum", 10); len(got) != 0 {
		t.Errorf("a term that matches nothing returned %d", len(got))
	}
	if got, _ = db.SearchTurns(ctx, "   ", 10); len(got) != 0 {
		t.Errorf("a blank term returned %d rows — it must not match everything", len(got))
	}
}

// LIKE's own wildcards must not leak out of the search box: searching for "%"
// would otherwise match every turn there is.
func TestSearchEscapesWildcards(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	inv, _ := db.CreateInvite(ctx, "Acme Corp", "", "tok", "hash-1", nil)

	for _, q := range []string{"Did margins hit 50% last year?", "What about growth"} {
		db.RecordTurn(ctx, &Turn{InviteID: inv.ID, SessionID: "v1",
			Question: q, Answer: "a", Usage: Usage{Model: "m"}, AskedAt: time.Now()})
	}

	got, err := db.SearchTurns(ctx, "50%", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Errorf(`searching "50%%" returned %d rows, want the one containing it`, len(got))
	}
	// A bare "%" matches the row that literally contains one, not every row.
	// Unescaped it would be a wildcard and return both.
	if got, _ = db.SearchTurns(ctx, "%", 10); len(got) != 1 {
		t.Errorf(`a bare "%%" matched %d rows, want 1 — the wildcard escaped`, len(got))
	}
	// Same for the single-character wildcard.
	if got, _ = db.SearchTurns(ctx, "_", 10); len(got) != 0 {
		t.Errorf(`"_" matched %d rows, want 0 — it is a wildcard unescaped`, len(got))
	}
}

// The retention window deletes turns and nothing else: a contact's links
// survive, and so do turns inside the window.
func TestPruneTurnsKeepsRecentTurnsAndAllLinks(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	inv, _ := db.CreateInvite(ctx, "Acme Corp", "", "tok", "hash-1", nil)
	now := time.Now()
	for _, age := range []time.Duration{40 * 24 * time.Hour, 20 * 24 * time.Hour, time.Hour} {
		if err := db.RecordTurn(ctx, &Turn{InviteID: inv.ID, SessionID: "v1",
			Question: "q", Answer: "a", Usage: Usage{Model: "m"}, AskedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.PruneTurns(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want the one older than 30 days", n)
	}
	turns, _ := db.TurnsForSession(ctx, "v1", 10)
	if len(turns) != 2 {
		t.Errorf("%d turns left, want 2", len(turns))
	}
	contacts, _ := db.ListContacts(ctx)
	if len(contacts) != 1 || len(contacts[0].Invites) != 1 {
		t.Error("pruning turns touched contacts or links")
	}
	// Nothing older: a second prune is a no-op, not an error.
	if n, err := db.PruneTurns(ctx, now.AddDate(0, 0, -30)); err != nil || n != 0 {
		t.Errorf("second prune = %d, %v", n, err)
	}
}
