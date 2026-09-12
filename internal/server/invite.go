package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/optionalsoftware/ask-about/internal/store"
)

// inviteCookie carries the link a visitor arrived on, so they can keep asking
// after navigating away from the /i/ URL.
//
// It holds the slug and the plaintext token — the same two halves as the URL —
// rather than the digest the database stores. Accepting a digest here would
// mean a copy of the database handed over working access, which is the whole
// reason only digests are stored.
const inviteCookie = "askabout_link"

// inviteCookieMaxAge outlives a browser session on purpose: someone who opens
// a link, closes the tab, and comes back next week should not need the original
// message again. The link's own expiry and revocation still apply on every
// request, so this only decides how long the browser remembers, never how long
// access lasts.
const inviteCookieMaxAge = 180 * 24 * time.Hour

// handleInvite serves an invite link.
//
// It always renders the page, whether or not the link is any good. An unfurler
// fetching this URL has no token, and refusing here would leave every preview
// in Slack and LinkedIn blank. Asking questions is what the gate covers, and
// that happens at the chat endpoint.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	slug, token := r.PathValue("name"), r.PathValue("token")

	// Remembered even when it does not resolve. A revoked link should say so
	// on the next visit rather than silently looking like an open site.
	http.SetCookie(w, &http.Cookie{
		Name:     inviteCookie,
		Value:    slug + "/" + token,
		Path:     "/",
		MaxAge:   int(inviteCookieMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})

	s.serveIndex(w, r)
}

// reasonNoLink marks the one refusal that is not a refusal of a link: the
// visitor never had one. It gets its own copy, because they hold no token and
// so there is nothing to give away by being useful to them.
const reasonNoLink = "no link"

// admission is the answer to "may this visitor ask a question, and whose
// budget does it come out of".
type admission struct {
	OK bool
	// InviteID is empty when the site is open, since there is no link to
	// attribute the turn to.
	InviteID string
	// Reason is for the log, never for the visitor. Telling someone which of
	// expired, revoked or unknown applies narrows a guess and changes nothing
	// they can do about it.
	Reason string
	// Message is what the visitor is shown. A bad link stays vague; a spend cap
	// does not, because the visitor did nothing wrong and the honest
	// explanation is the one they can act on.
	Message string
}

// admit decides whether a request may ask a question.
//
// An open site admits everyone. Invite-only resolves the cookie, and the check
// runs on every request rather than once at arrival, so revoking a link stops
// the next question rather than the next visit.
func (s *Server) admit(ctx context.Context, r *http.Request) admission {
	adm := s.admitLink(ctx, r)
	if !adm.OK {
		return adm
	}
	if capped := s.overBudget(ctx, r); capped != nil {
		capped.InviteID = adm.InviteID
		return *capped
	}
	return adm
}

// admitLink resolves the invite, ignoring spend.
func (s *Server) admitLink(ctx context.Context, r *http.Request) admission {
	if !s.inviteOnly {
		return admission{OK: true}
	}
	if s.store == nil {
		// invite_only with no storage would refuse everyone forever. Caught at
		// startup; this is the belt to that braces.
		return admission{Reason: "invite_only is set but there is no store", Message: s.deniedMsg}
	}

	slug, token, ok := inviteFromCookie(r)
	if !ok {
		return admission{Reason: reasonNoLink, Message: s.noLinkMsg}
	}

	inv, err := s.store.InviteByTokenHash(ctx, hashToken(slug, token))
	if err != nil {
		s.log.Error("could not resolve invite", "err", err)
		return admission{Reason: "lookup failed", Message: s.deniedMsg}
	}
	switch {
	case inv == nil:
		// Also what a tampered slug looks like: the name is hashed into the
		// digest, so editing it resolves to nothing.
		return admission{Reason: "unknown link", Message: s.deniedMsg}
	case inv.RevokedAt != nil:
		return admission{Reason: "revoked", InviteID: inv.ID, Message: s.deniedMsg}
	case inv.ExpiresAt != nil && time.Now().After(*inv.ExpiresAt):
		return admission{Reason: "expired", InviteID: inv.ID, Message: s.deniedMsg}
	}
	return admission{OK: true, InviteID: inv.ID}
}

// inviteFromCookie splits the stored "slug/token" pair. The slug may itself
// contain no slash — slugify guarantees that — so a single split is enough.
func inviteFromCookie(r *http.Request) (slug, token string, ok bool) {
	c, err := r.Cookie(inviteCookie)
	if err != nil {
		return "", "", false
	}
	slug, token, found := strings.Cut(c.Value, "/")
	if !found || slug == "" || token == "" {
		return "", "", false
	}
	return slug, token, true
}

// overBudget checks the spend caps, returning nil when there is room.
//
// Both caps compare what has ALREADY been spent against the limit, because the
// cost of the next answer is not knowable until it has been generated. One
// question therefore crosses the line rather than stopping exactly on it —
// worth roughly two cents here, and the alternative is refusing on a guess.
func (s *Server) overBudget(ctx context.Context, r *http.Request) *admission {
	if s.store == nil || (s.maxPerSession <= 0 && s.maxPerDay <= 0) {
		return nil
	}

	// The day cap is checked first: it is the backstop, and a site that has
	// spent its day should say so rather than blaming the visitor's visit.
	if s.maxPerDay > 0 {
		usage, err := s.store.UsageSince(ctx, startOfDay(time.Now()))
		if err != nil {
			// Failing open is deliberate. A broken query must not take the site
			// down, and the day cap is a backstop against runaway spend rather
			// than a hard ledger — the vendor console remains the real limit.
			s.log.Error("could not total today's usage; not enforcing the daily cap", "err", err)
		} else if spent := s.total(usage); spent >= s.maxPerDay {
			return &admission{
				Reason:  fmt.Sprintf("daily cap: $%.4f of $%.2f", spent, s.maxPerDay),
				Message: s.dayCapMsg,
			}
		}
	}

	// A visitor with cookies off carries no session, so there is no visit to
	// total and this cap cannot apply to them. The daily cap above still does.
	if session, err := r.Cookie(sessionCookie); s.maxPerSession > 0 && err == nil {
		usage, err := s.store.SessionUsage(ctx, session.Value)
		if err != nil {
			s.log.Error("could not total this visit; not enforcing the session cap", "err", err)
		} else if spent := s.total(usage); spent >= s.maxPerSession {
			return &admission{
				Reason:  fmt.Sprintf("session cap: $%.4f of $%.2f", spent, s.maxPerSession),
				Message: s.sessionCapMsg,
			}
		}
	}
	return nil
}

// total prices usage grouped by model. A model with no configured rate
// contributes nothing, which is why startup refuses to run caps without rates
// — otherwise a cap would quietly never trip.
func (s *Server) total(usage []store.Usage) float64 {
	var sum float64
	for _, u := range usage {
		if cost, ok := s.cost(u); ok {
			sum += cost
		}
	}
	return sum
}

// startOfDay is midnight local time, so "today" means what it means to whoever
// is reading the admin pages rather than to UTC.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
