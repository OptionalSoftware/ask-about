package server

import (
	"net/http"
)

// sessionCookie marks one visit. It carries no expiry, so the browser drops it
// when it closes — the visitor draws the boundary by leaving, rather than the
// server guessing one from gaps between timestamps.
const sessionCookie = "askabout_visit"

// sessionID returns the visit id for this request, issuing one if the request
// arrived without a usable cookie.
//
// The value is opaque and random: it identifies a visit, not a person, and
// nothing is stored under it beyond the turns already being recorded. It is
// not a credential, so it does no work beyond grouping.
func (s *Server) sessionID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && validSessionID(c.Value) {
		return c.Value
	}

	id, err := newToken()
	if err != nil {
		// Grouping is a nicety; the turn still gets recorded without it.
		s.log.Warn("could not generate a session id", "err", err)
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: id,
		Path:  "/",
		// No MaxAge or Expires: this is a session cookie by design.
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Set only over TLS, but not when running plain HTTP locally — a
		// Secure cookie on http:// is dropped, which would silently put every
		// turn of a local session on its own visit.
		Secure: isHTTPS(r),
	})
	return id
}

// validSessionID rejects anything not shaped like one we issued, so a
// hand-edited cookie cannot invent an arbitrary grouping key or smuggle
// unexpected bytes into a stored row.
func validSessionID(v string) bool {
	if len(v) != tokenLength {
		return false
	}
	for _, c := range v {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
