package server

import (
	"io/fs"
	"log/slog"
	"net/http"
)

// prompt is one interview prompt offered on the admin page: which file it
// is, and how the page describes it.
type prompt struct {
	Slug  string
	Title string
	Blurb string
	// Text is the prompt itself, read from the embedded file at startup.
	// Empty means the file was not supplied, and the prompt is not served.
	Text string
}

// catalogue lists the prompts by kind of subject. The slug is the file name
// under prompts/, less the "interview-" prefix and the extension.
var catalogue = map[bool][]prompt{
	false: { // a person, by level
		{Slug: "ic", Title: "Individual Contributor",
			Blurb: "Your work is the thing you build or deliver."},
		{Slug: "senior-ic", Title: "Senior Individual Contributor",
			Blurb: "Staff or principal — you set direction across teams without owning the headcount."},
		{Slug: "manager", Title: "Manager",
			Blurb: "You run one team."},
		{Slug: "executive", Title: "Executive",
			Blurb: "You run an organisation of teams and manage other leaders."},
	},
	true: { // a product, or a company
		{Slug: "product", Title: "Product or Service",
			Blurb: "Something people sign up for or buy — software, an app, an API: plans, features, limits, support."},
		{Slug: "company", Title: "Company or Organisation",
			Blurb: "A firm or team that does work for clients — including consultancies and agencies: services, past work, how to engage, working there."},
	},
}

// loadPrompts reads this kind's prompts from the supplied filesystem. A
// missing file is logged and the entry served without text, so the page still
// renders and says what is missing rather than failing outright.
func loadPrompts(files fs.FS, product bool, log *slog.Logger) []prompt {
	out := make([]prompt, 0, len(catalogue[product]))
	for _, p := range catalogue[product] {
		if files != nil {
			b, err := fs.ReadFile(files, "interview-"+p.Slug+".md")
			if err != nil {
				log.Warn("interview prompt not available", "slug", p.Slug, "err", err)
			}
			p.Text = string(b)
		}
		out = append(out, p)
	}
	return out
}

// documentView is the Your Document page.
type documentView struct {
	Nav     []navLink
	Prompts []prompt
	// Product changes the framing: a person is interviewed about
	// themselves, a product's owner about the product.
	Product bool
}

func (s *Server) handleDocument(w http.ResponseWriter, r *http.Request) {
	s.render(w, "document.html", documentView{Nav: s.nav("/document"), Prompts: s.prompts, Product: s.product})
}

// handleDocumentPrompt serves one prompt as plain text, for reading in the
// browser and for the copy button, which fetches it rather than carrying
// several kilobytes of prompt in the page.
func (s *Server) handleDocumentPrompt(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	for _, p := range s.prompts {
		if p.Slug == slug && p.Text != "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(p.Text))
			return
		}
	}
	http.NotFound(w, r)
}
