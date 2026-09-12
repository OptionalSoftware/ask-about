// Package store persists conversations and the token accounting behind them.
//
// The interface is domain-shaped rather than SQL-shaped: no query strings, no
// *sql.Rows, nothing that leaks the backend. Swapping SQLite for Postgres
// should be a new file in this package, not a change anywhere else.
//
// Operations are deliberately coarse. Fine-grained methods force transactions
// through the interface, which is where designs like this usually go wrong.
package store

import (
	"context"
	"time"
)

// Contact is whoever a link was made for — a person, a company, a job posting.
// The useful identifier depends on how they reached you, so it is free text
// rather than an email: a name from LinkedIn, a company and role from an
// application form.
//
// Normally a contact has one link and keeps using it. A replacement issued
// after the first is lost or used up attaches here, so the history stays in
// one place.
type Contact struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Invite is one link.
//
// TokenHash is stored rather than the token itself, so a copy of this database
// does not hand over working links.
type Invite struct {
	ID        string
	ContactID string
	TokenHash string
	// Token is the plaintext half of the link, kept so it can be sent again.
	// Empty on invites created before it was stored — those cannot be shown
	// and have to be reissued.
	Token string
	// Note is private. The contact's name appears in the URL; this never does.
	Note      string
	CreatedAt time.Time
	// ExpiresAt is nil for a link that does not expire, which is the default:
	// a link is normally retired by revoking it or by hitting its budget.
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// Session is one visit — the turns taken before the browser was closed.
// Bounded by a cookie rather than by a gap between timestamps, so it reflects
// the visitor leaving rather than a threshold picked here.
type Session struct {
	ID      string
	Started time.Time
	Ended   time.Time
	Turns   []Turn
	Usage   Usage
}

// Usage is the token accounting for one turn, kept as four separate counts
// because they price differently. Cost is derived from these at read time, so
// a wrong rate in config can be corrected retroactively.
type Usage struct {
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
	Model            string
}

// Turn is one question and its answer.
type Turn struct {
	ID     string
	Vendor string
	// InviteID is empty for turns taken outside an invite, which is every turn
	// until invite gating exists.
	InviteID string
	// SessionID groups turns into one visit. Empty when the visitor arrived
	// with cookies disabled, which puts each of their turns on its own visit.
	SessionID string
	Question  string
	Answer    string
	Usage     Usage
	Latency   time.Duration
	// Blocked counts sentences the guard withheld. Zero while the guard is a
	// pass-through, but recorded from the start so the column is already there.
	Blocked int
	// Err is the failure message when a turn did not complete. Failed turns are
	// still recorded — they cost tokens and still show what people asked.
	Err     string
	AskedAt time.Time
}

// InviteDetail is an invite joined to its contact and its usage to date.
// Assembling it in the store lets SQLite do the aggregation instead of the
// caller looping over turns.
type InviteDetail struct {
	Invite
	Name  string
	Usage Usage
	Turns int64
	// Sessions counts distinct visits on this link.
	Sessions int64
	// LastSeen is the most recent turn on this invite, zero if never used.
	LastSeen time.Time
}

// ContactDetail is a contact with its links and its totals across all of them,
// which is what the list page shows.
type ContactDetail struct {
	Contact
	Invites  []InviteDetail
	Usage    Usage
	Turns    int64
	Sessions int64
	LastSeen time.Time
}

// TurnMatch is one question found by a search, with enough around it to be
// worth reading: who asked, and which link to open to see it in context.
type TurnMatch struct {
	Turn
	ContactName string
}

// Store is the persistence seam. Implementations must be safe for concurrent
// use by multiple goroutines.
type Store interface {
	// RecordTurn saves a completed turn. It assigns the ID.
	RecordTurn(ctx context.Context, t *Turn) error

	// CreateInvite records a link for the named contact, creating the contact
	// on first sight and reusing it afterwards — so a replacement link lands
	// under the same name rather than forking the history. It assigns the IDs.
	//
	// expires may be nil for a link that does not expire.
	//
	// tokenHash is stored as given: hashing belongs to the caller that knows
	// the plaintext token, and it must never reach this package.
	CreateInvite(ctx context.Context, name, note, token, tokenHash string, expires *time.Time) (*Invite, error)

	// InviteByTokenHash resolves a link. Returns nil without error when no
	// invite matches, since an unknown token is an ordinary refusal.
	InviteByTokenHash(ctx context.Context, tokenHash string) (*InviteDetail, error)

	// ListContacts returns every contact with its links and totals, most
	// recently active first.
	ListContacts(ctx context.Context) ([]ContactDetail, error)

	// ContactNames returns the names already in use, for the create form's
	// suggestions — picking an existing name is what keeps a replacement link
	// attached to the same contact.
	ContactNames(ctx context.Context) ([]string, error)

	// SessionsForInvite returns the visits on one link, newest first, each
	// with the turns taken during it.
	SessionsForInvite(ctx context.Context, inviteID string) ([]Session, error)

	// TurnsForSession returns the last limit completed turns of one visit,
	// oldest first. This is where a conversation's history comes from: the
	// answers here are ones this server produced, unlike the copy the browser
	// holds, which is whatever the visitor last sent us.
	TurnsForSession(ctx context.Context, sessionID string, limit int) ([]Turn, error)

	// SessionUsage totals the tokens spent so far in one visit, grouped by
	// model. Used to enforce a per-visit spend cap.
	SessionUsage(ctx context.Context, sessionID string) ([]Usage, error)

	// UsageSince totals tokens across every visitor from a moment onward,
	// grouped by model. Used to enforce a daily spend cap.
	UsageSince(ctx context.Context, since time.Time) ([]Usage, error)

	// UsageBetween totals the half-open window [from, to), grouped by model.
	// Callers pass local-midnight boundaries; see the implementation for why
	// the day is not grouped in SQL.
	UsageBetween(ctx context.Context, from, to time.Time) ([]Usage, error)

	// SearchTurns finds questions and answers containing term, newest first,
	// with the contact each belongs to.
	SearchTurns(ctx context.Context, term string, limit int) ([]TurnMatch, error)

	// UpdateInvite changes the two things about a link that are worth changing
	// after it has been sent: its private note and when it expires.
	//
	// Both are written every time, so a nil expires clears an expiry rather
	// than leaving the old one in place. The caller submits the whole state of
	// the link, not a patch.
	UpdateInvite(ctx context.Context, id, note string, expires *time.Time) error

	// RevokeInvite marks a link dead. Revoking is a stamp rather than a delete
	// so its turns keep pointing at something.
	RevokeInvite(ctx context.Context, id string) error

	Close() error
}
