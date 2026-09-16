package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/optionalsoftware/ask-about/internal/config"
)

// adminAuth gates the admin pages on an optional address restriction and a
// username and password.
//
// The address is checked first: it is cheaper, and it means a caller from the
// wrong network is refused without learning whether a guess at the password
// was right.
type adminAuth struct {
	user, pass []byte
	allowed    netip.Prefix
	restricted bool
	trusted    *netip.Prefix
	lock       *lockout
	log        *slog.Logger
}

func newAdminAuth(cfg config.Admin, trusted *netip.Prefix, log *slog.Logger) (*adminAuth, error) {
	allowed, restricted, err := cfg.AllowedNet()
	if err != nil {
		return nil, err
	}
	return &adminAuth{
		user:       []byte(cfg.Username),
		pass:       []byte(cfg.Password),
		allowed:    allowed,
		restricted: restricted,
		trusted:    trusted,
		lock:       newLockout(cfg.MaxAttempts, config.LockoutWindow),
		log:        log,
	}, nil
}

func (a *adminAuth) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.addressAllowed(r) {
			// Logged with the address as seen, because the usual way to be
			// locked out is admin.ip holding an address that is not the one
			// arriving. This line is what tells you which to put there.
			seen, _ := clientAddr(r, a.trusted)
			a.log.Warn("admin request from disallowed address",
				"client", seen.String(), "allowed", a.allowed.String(),
				"remote", r.RemoteAddr, "forwarded_for", r.Header.Get("X-Forwarded-For"),
				"path", r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		addr, _ := clientAddr(r, a.trusted)

		if a.lock.lockedOut(addr) {
			a.log.Warn("admin login locked out", "client", addr.String())
			// 429 rather than 401: the browser would otherwise re-prompt for a
			// password that cannot currently work.
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}

		if !a.credentialsMatch(r) {
			a.lock.failed(addr)
			a.log.Warn("admin login failed", "client", addr.String())
			w.Header().Set("WWW-Authenticate", `Basic realm="ask-about admin", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.lock.succeeded(addr)

		// A cross-site form post carries the browser's saved Basic auth
		// credentials automatically, so being logged in is enough for another
		// page to act as you. Origin is sent on every cross-origin POST, so a
		// mismatch is the signal. Its absence is not: a request without one is
		// not from a browser form, and CSRF needs a browser.
		if !sameOrigin(r) {
			a.log.Warn("admin request from another origin",
				"origin", r.Header.Get("Origin"), "host", r.Host, "path", r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// addressAllowed compares the client's address against the configured prefix.
func (a *adminAuth) addressAllowed(r *http.Request) bool {
	if !a.restricted {
		return true
	}
	addr, ok := clientAddr(r, a.trusted)
	if !ok {
		return false
	}
	return a.allowed.Contains(addr.Unmap())
}

// clientAddr works out who is actually calling, whether ask-about is on the
// internet by itself or behind a proxy.
//
// The connection address is the client unless the connection comes from the
// trusted proxy, in which case X-Forwarded-For is read — its RIGHTMOST entry,
// because a proxy appends the address it saw, so the last entry is the one it
// vouches for and everything to its left is whatever the caller claimed.
// Reading the leftmost, the common mistake, would take the forgery.
//
// With no trusted proxy configured the header is ignored entirely. Believing
// it from anyone would let a caller who reaches the binary directly pick a
// fresh address per request, which is exactly what the lockout counts on.
func clientAddr(r *http.Request, trusted *netip.Prefix) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	conn, err := netip.ParseAddr(host)
	if err != nil {
		return conn, false
	}
	if trusted == nil || !trusted.Contains(conn.Unmap()) {
		return conn, true
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if addr, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			return addr, true
		}
	}
	return conn, true
}

// credentialsMatch compares in constant time, and hashes first so that the
// comparison is over fixed-length inputs and cannot leak the length of the
// configured password through timing.
func (a *adminAuth) credentialsMatch(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	wantUser, wantPass := sha256.Sum256(a.user), sha256.Sum256(a.pass)
	gotUser, gotPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) == 1 &&
		subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) == 1
}

// sameOrigin reports whether a state-changing request came from ask-about's own
// pages rather than someone else's.
func sameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin means no browser form, and therefore no CSRF. Rejecting
		// here would break curl and anything scripted for no security gain.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}
