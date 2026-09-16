package server

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/optionalsoftware/ask-about/config"
)

// The tab icon: the subject's initial on the accent tile, with the avatar's
// scanlines behind it.
//
// Built from config rather than shipped as a file, because the whole point of
// this thing is that it can be pointed at someone else — a checked-in icon
// would be one more place the old subject's name lingers. It is rendered once
// at startup; there is nothing per-request about it.
//
// Declaring an icon at all is what stops the browser asking the domain root
// for /favicon.ico, which ask-about deliberately does not serve.
//
// No dark-mode variant. A favicon sits on browser chrome rather than on the
// page, so it has to work against either — hence a solid tile instead of
// anything that lets a background show through.
const faviconTmpl = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="%s">
  <rect width="64" height="64" rx="14" fill="#2f6f5e"/>
  <g fill="#ffffff" opacity="0.13">
    <rect y="12" width="64" height="2"/><rect y="22" width="64" height="2"/>
    <rect y="32" width="64" height="2"/><rect y="42" width="64" height="2"/>
    <rect y="52" width="64" height="2"/>
  </g>
  <text x="32" y="34" fill="#ffffff" font-size="40" font-weight="700"
        text-anchor="middle" dominant-baseline="central"
        font-family="ui-sans-serif, system-ui, Helvetica, Arial, sans-serif">%s</text>
</svg>
`

// buildFavicon renders the icon for a subject.
func buildFavicon(s config.Subject) []byte {
	// FullName covers a product's single name as well as first-then-last.
	initial := firstInitial(s.FullName())
	if initial == "" {
		// Nothing usable to draw. A question mark still reads as "ask
		// something here", which is closer to right than a blank tile.
		initial = "?"
	}
	return []byte(fmt.Sprintf(faviconTmpl, initial, initial))
}

// firstInitial takes the first letter or digit of a name, uppercased.
//
// Restricted to those two classes on purpose: the result is dropped into SVG
// unescaped, and a name is free text from a config file. Anything that is not
// a letter or a digit cannot open a tag or close an attribute, and is skipped
// here rather than escaped — the alternative is carrying an escaper for a
// single character.
func firstInitial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
		return ""
	}
	return ""
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	// Revalidate rather than cache hard: the name comes from a config file
	// that an operator can change and restart, and a pinned tab icon with no
	// way to invalidate it is a bad trade for a few hundred bytes.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.favicon)
}
