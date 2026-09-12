package server

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// Preview is what a link unfurls into in Slack, LinkedIn, iMessage and the
// rest, and what the browser tab says.
//
// These crawlers arrive with no cookie, no token and no JavaScript. Whatever
// they should show has to be in the HTML the server sends — the client fills
// the same values in later, which is far too late for them.
type Preview struct {
	Title       string
	Description string
	// ImagePath is a file on disk, served by handlePreviewImage. Empty means
	// the unfurl carries no thumbnail, which beats one pointing at a 404.
	ImagePath string
}

// previewData is what index.html is rendered with. Image and URL are absolute
// because a crawler cannot resolve a relative path against a page it fetched
// out of a chat message.
type previewData struct {
	Title       string
	Description string
	Image       string
	URL         string
}

// indexTemplate caches the parsed page. Reparsed on every request in dev so
// edits to index.html show up without a restart.
type indexTemplate struct {
	once sync.Once
	tmpl *template.Template
	err  error
}

func (s *Server) indexHTML() (*template.Template, error) {
	parse := func() (*template.Template, error) {
		raw, err := fs.ReadFile(s.web, "index.html")
		if err != nil {
			return nil, err
		}
		return template.New("index.html").Parse(string(raw))
	}
	if s.dev {
		return parse()
	}
	s.index.once.Do(func() { s.index.tmpl, s.index.err = parse() })
	return s.index.tmpl, s.index.err
}

// serveIndex renders the page with its preview metadata filled in.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	tmpl, err := s.indexHTML()
	if err != nil {
		s.log.Error("could not parse index.html", "err", err)
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}

	origin := requestOrigin(r)
	data := previewData{
		Title:       s.preview.Title,
		Description: s.preview.Description,
		// The path the visitor actually arrived on, so an unfurled invite link
		// points back at itself rather than at the bare site.
		URL: origin + r.URL.EscapedPath(),
	}
	if s.preview.ImagePath != "" {
		data.Image = origin + previewImagePath
	}

	// Buffered so a template error is an honest 500 rather than half a page
	// sent with a 200.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		s.log.Error("could not render index.html", "err", err)
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(buf.Bytes())
}

// previewImagePath is where the unfurl thumbnail lives. Deliberately outside
// any gate: a crawler has no token, so an image behind one silently produces
// previews with no picture.
const previewImagePath = Base + "/preview.jpg"

func (s *Server) handlePreviewImage(w http.ResponseWriter, r *http.Request) {
	if s.preview.ImagePath == "" {
		http.NotFound(w, r)
		return
	}
	// Unfurlers cache aggressively anyway, and this changes about never.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, s.preview.ImagePath)
}

// requestOrigin reconstructs the scheme and host the visitor used, which is
// the only way to build the absolute URLs an unfurler needs. Behind a proxy
// the scheme is only knowable from X-Forwarded-Proto; getting it wrong means
// an https page advertising http assets, which unfurlers drop.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return scheme + "://" + host
}
