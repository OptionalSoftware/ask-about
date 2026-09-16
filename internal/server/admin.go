package server

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
	"github.com/optionalsoftware/ask-about/internal/store"
)

//go:embed admin/*.html
var adminFS embed.FS

var adminTmpl = template.Must(template.New("admin").Funcs(template.FuncMap{
	"comma": comma,
	"date":  func(t time.Time) string { return t.Local().Format("2 Jan 2006") },
	"day":   func(t time.Time) string { return t.Local().Format("2 Jan") },
	"pct":   func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
	"stamp": func(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") },
	// isodate fills a date input, which only accepts this one format. Nil is
	// an empty box, which is how "no expiry" is both shown and set.
	"isodate": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Local().Format("2006-01-02")
	},
	"clock": func(t time.Time) string { return t.Local().Format("15:04") },
	"money": func(f float64) string { return "$" + strconv.FormatFloat(f, 'f', 2, 64) },
}).ParseFS(adminFS, "admin/*.html"))

// listView is the contacts page.
type listView struct {
	Contacts []contactRow
	// Query is what was typed in the search box, echoed back so the box keeps
	// its contents after the page reloads.
	Query string
	// Matches are questions containing Query. Empty unless something was
	// searched for.
	Matches []store.TurnMatch
	// Searched distinguishes "no results" from "no search", which otherwise
	// look identical to the template.
	Searched bool
	// Spend is nil when there is nothing to total, which is only when storage
	// is off — and then there is no admin page at all.
	Spend *spendView
	// Names are the contacts already on file, offered as suggestions. Picking
	// one is what keeps a replacement link under the same contact rather than
	// starting a second entry that looks like a stranger.
	Names []string
	// Issued is a freshly created link, shown at the top so it can be copied
	// without hunting for its row. It is also listed below like any other.
	Issued string
	Error  string
}

type contactRow struct {
	store.ContactDetail
	Links  []inviteRow
	Cost   float64
	Priced bool
}

type inviteRow struct {
	store.InviteDetail
	// URL is the link as sent, rebuilt from the stored token. Empty for
	// invites created before the token was kept — those can only be replaced.
	URL    string
	Cost   float64
	Status string
	// Priced is false when no rate is configured for the model, so the page
	// can say so instead of showing a confident $0.00.
	Priced bool
}

// detailView is one link: its settings and its visit history.
type detailView struct {
	Name     string
	Link     inviteRow
	Sessions []sessionRow
	// Saved and Error report the result of the last save, carried back through
	// the redirect that stops a refresh from re-submitting it.
	Saved bool
	Error string
}

type sessionRow struct {
	store.Session
	Cost   float64
	Priced bool
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.renderList(w, r, listView{})
}

func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderList(w, r, listView{Error: "A name is required — it is what the link is filed under."})
		return
	}
	if slugify(name) == "" {
		s.renderList(w, r, listView{Error: "That name has no letters or digits to put in a URL."})
		return
	}

	// Blank means no expiry, which is the common case: a link is normally
	// retired by revoking it or by running out of budget, not by a calendar.
	var expires *time.Time
	if v := strings.TrimSpace(r.FormValue("days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.renderList(w, r, listView{Error: "Expiry must be a whole number of days, or blank for none."})
			return
		}
		t := time.Now().AddDate(0, 0, n)
		expires = &t
	}

	token, err := newToken()
	if err != nil {
		s.log.Error("admin: generate token", "err", err)
		s.renderList(w, r, listView{Error: "Could not generate a token."})
		return
	}

	if _, err := s.store.CreateInvite(r.Context(), name,
		r.FormValue("note"), token, hashToken(name, token), expires); err != nil {
		s.log.Error("admin: create invite", "err", err)
		s.renderList(w, r, listView{Error: "Could not create the link."})
		return
	}

	s.renderList(w, r, listView{Issued: inviteURL(r, name, token)})
}

// handleAdminUpdate saves the note and expiry of an existing link.
//
// Both fields are written on every save: the form carries the whole state of
// the link, so clearing the date box is how an expiry is removed.
func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	back := Base + "/admin/links/" + url.PathEscape(id)

	expires, err := parseExpiry(r.FormValue("expires"))
	if err != nil {
		http.Redirect(w, r, back+"?err=date", http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateInvite(r.Context(), id, r.FormValue("note"), expires); err != nil {
		s.log.Error("admin: update invite", "err", err)
		http.Redirect(w, r, back+"?err=save", http.StatusSeeOther)
		return
	}
	// Redirect so a refresh does not re-submit the save.
	http.Redirect(w, r, back+"?saved=1", http.StatusSeeOther)
}

// parseExpiry reads the date box. Blank means no expiry.
//
// The moment returned is the end of the chosen day in local time, so a link
// set to expire on the 14th works for all of the 14th — and the date the admin
// page shows back is the one that was typed.
func parseExpiry(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil, err
	}
	t := d.AddDate(0, 0, 1).Add(-time.Second)
	return &t, nil
}

func (s *Server) handleAdminRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeInvite(r.Context(), r.FormValue("id")); err != nil {
		s.log.Error("admin: revoke invite", "err", err)
		s.renderList(w, r, listView{Error: "Could not revoke that link."})
		return
	}
	// Redirect so a refresh does not re-submit the revoke.
	http.Redirect(w, r, Base+"/admin", http.StatusSeeOther)
}

// handleAdminLink shows one link's visits: which days they came back, and what
// they asked each time.
func (s *Server) handleAdminLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	contacts, err := s.store.ListContacts(r.Context())
	if err != nil {
		s.log.Error("admin: list contacts", "err", err)
		http.Error(w, "could not load links", http.StatusInternalServerError)
		return
	}

	view := detailView{Saved: r.URL.Query().Get("saved") == "1"}
	switch r.URL.Query().Get("err") {
	case "date":
		view.Error = "That date could not be read. Use the picker, or clear it for no expiry."
	case "save":
		view.Error = "Could not save that."
	}

	var found bool
	for _, c := range contacts {
		for _, inv := range c.Invites {
			if inv.ID == id {
				view.Name, view.Link, found = c.Name, s.priceInvite(r, inv), true
			}
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	sessions, err := s.store.SessionsForInvite(r.Context(), id)
	if err != nil {
		s.log.Error("admin: sessions", "err", err)
		http.Error(w, "could not load visits", http.StatusInternalServerError)
		return
	}
	for _, sess := range sessions {
		row := sessionRow{Session: sess}
		row.Cost, row.Priced = s.cost(sess.Usage)
		view.Sessions = append(view.Sessions, row)
	}

	s.render(w, "detail.html", view)
}

func (s *Server) renderList(w http.ResponseWriter, r *http.Request, view listView) {
	contacts, err := s.store.ListContacts(r.Context())
	if err != nil {
		s.log.Error("admin: list contacts", "err", err)
		http.Error(w, "could not load contacts", http.StatusInternalServerError)
		return
	}
	if view.Names, err = s.store.ContactNames(r.Context()); err != nil {
		s.log.Error("admin: contact names", "err", err)
	}

	view.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if view.Query != "" {
		view.Searched = true
		if view.Matches, err = s.store.SearchTurns(r.Context(), view.Query, 50); err != nil {
			s.log.Error("admin: search", "err", err)
		}
	}

	view.Spend = s.buildSpend(r.Context())
	view.Contacts = make([]contactRow, 0, len(contacts))
	for _, c := range contacts {
		row := contactRow{ContactDetail: c}
		row.Cost, row.Priced = s.cost(c.Usage)
		for _, inv := range c.Invites {
			row.Links = append(row.Links, s.priceInvite(r, inv))
		}
		view.Contacts = append(view.Contacts, row)
	}

	s.render(w, "list.html", view)
}

// render writes the page only once it has rendered completely.
//
// Executing straight to the ResponseWriter streams as it goes, so a template
// error partway through leaves a truncated page already sent with a 200 — it
// looks like the data simply ran out. Buffering costs a few kilobytes and
// turns that into an honest 500.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := adminTmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("admin: render", "template", name, "err", err)
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) priceInvite(r *http.Request, d store.InviteDetail) inviteRow {
	row := inviteRow{InviteDetail: d, Status: inviteStatus(d, time.Now())}
	row.Cost, row.Priced = s.cost(d.Usage)
	if d.Token != "" {
		row.URL = inviteURL(r, d.Name, d.Token)
	}
	return row
}

// cost prices usage, reporting whether a rate was configured at all — an
// unpriced model must read as "no rates" rather than as free.
func (s *Server) cost(u store.Usage) (float64, bool) {
	// Nothing used is $0.00, not "no rates": a link nobody has opened yet has
	// no model to price and should not look misconfigured.
	if u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens+u.OutputTokens == 0 {
		return 0, true
	}
	rates, ok := s.pricing.For(u.Model)
	if !ok || rates.Zero() {
		return 0, false
	}
	return rates.EstimateUSD(u.InputTokens, u.CacheReadTokens,
		u.CacheWriteTokens, u.OutputTokens), true
}

func inviteStatus(d store.InviteDetail, now time.Time) string {
	switch {
	case d.RevokedAt != nil:
		return "revoked"
	case d.ExpiresAt != nil && now.After(*d.ExpiresAt):
		return "expired"
	case d.Turns == 0:
		return "unused"
	default:
		return "active"
	}
}

// inviteURL builds the link handed to a visitor. The name is visible in the
// path on purpose: someone forwarding it can see whose it is, which
// discourages passing it around more than any technical control would. It is
// also why the note is a separate field — that half stays private.
func inviteURL(r *http.Request, name, token string) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + Base + "/i/" + slugify(name) + "/" + token
}

// comma groups a count so six figures can be read at a glance.
//
// It takes any so a template can pass either a stored int64 or the result of
// len. A mismatch there is a render-time error, which is a bad way to find a
// typo — and the page is already half-written by the time it surfaces.
func comma(v any) string {
	var n int64
	switch t := v.(type) {
	case int64:
		n = t
	case int:
		n = int64(t)
	default:
		return fmt.Sprint(v)
	}
	if n < 0 {
		return "-" + comma(-n)
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// adminRoutes registers the admin surface behind auth. Nothing is registered
// when admin is disabled or storage is off, so an unconfigured deploy has no
// admin surface to find rather than one that merely refuses.
func (s *Server) adminRoutes(mux *http.ServeMux, cfg config.Admin) error {
	if !cfg.Enabled() || s.store == nil {
		return nil
	}
	auth, err := newAdminAuth(cfg, s.trusted, s.log)
	if err != nil {
		return err
	}
	// Registered route by route rather than behind a "/admin/" wildcard: the
	// wildcard matches every method and so conflicts with the method-scoped
	// "GET /" that serves the chat page.
	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + Base + "/admin":                 s.handleAdmin,
		"GET " + Base + "/admin/document":        s.handleDocument,
		"GET " + Base + "/admin/document/{slug}": s.handleDocumentPrompt,
		"GET " + Base + "/admin/links/{id}":      s.handleAdminLink,
		"POST " + Base + "/admin/links":          s.handleAdminCreate,
		"POST " + Base + "/admin/links/revoke":   s.handleAdminRevoke,
		"POST " + Base + "/admin/links/{id}":     s.handleAdminUpdate,
	} {
		mux.Handle(pattern, auth.wrap(h))
	}
	return nil
}

// spendDays is how much history the strip shows. Two weeks is enough to see a
// trend without turning the top of the page into a chart.
const spendDays = 14

// spend is one day's cost, for the strip at the top of the list page.
type spend struct {
	Day   time.Time
	Cost  float64
	Today bool
	// Pct is the day's share of the tallest bar, so the strip scales to
	// whatever the traffic actually was rather than to the cap.
	Pct float64
}

// spendView is what the strip renders.
type spendView struct {
	Days []spend
	// Today is the running total the daily cap is compared against, so the
	// number here and the number that closes the site are the same number.
	Today float64
	Cap   float64
	// CapPct is today against the cap, clamped, for the meter.
	CapPct float64
	// Near is true once today is most of the way to the cap — the point at
	// which this stops being trivia and starts being a warning.
	Near bool
	// Priced is false when no rate covers the models used, so the strip can
	// say so rather than showing a confident zero.
	Priced bool
}

// buildSpend totals the last spendDays days, one local day at a time.
//
// A query per day rather than one grouped query: the boundary that matters is
// local midnight, and the timestamps are stored in UTC, so grouping in SQL
// would bucket by UTC date and disagree with the cap about when "today" began.
func (s *Server) buildSpend(ctx context.Context) *spendView {
	if s.store == nil {
		return nil
	}

	view := &spendView{Cap: s.maxPerDay}
	midnight := startOfDay(time.Now())

	var tallest float64
	var anyUsage bool
	for i := spendDays - 1; i >= 0; i-- {
		from := midnight.AddDate(0, 0, -i)
		usage, err := s.store.UsageBetween(ctx, from, from.AddDate(0, 0, 1))
		if err != nil {
			s.log.Error("could not total a day's usage", "day", from, "err", err)
			return nil
		}
		for _, u := range usage {
			anyUsage = true
			if _, ok := s.cost(u); ok {
				view.Priced = true
			}
		}
		d := spend{Day: from, Cost: s.total(usage), Today: i == 0}
		if d.Cost > tallest {
			tallest = d.Cost
		}
		view.Days = append(view.Days, d)
	}

	// "No rates" means a model was used that has no [pricing] block. Before any
	// question has been asked there is nothing unpriced, and a fresh install
	// should read $0.00 rather than look misconfigured. Keyed on rows, not on
	// cost: unpriced usage also totals zero, and that is the case the label
	// exists to flag.
	if !anyUsage {
		view.Priced = true
	}

	if tallest > 0 {
		for i := range view.Days {
			view.Days[i].Pct = view.Days[i].Cost / tallest * 100
		}
	}
	if n := len(view.Days); n > 0 {
		view.Today = view.Days[n-1].Cost
	}
	if view.Cap > 0 {
		view.CapPct = min(view.Today/view.Cap*100, 100)
		// Three quarters is far enough along that the rest of the day is at
		// risk, and early enough to do something about it.
		view.Near = view.Today >= view.Cap*0.75
	}
	return view
}
