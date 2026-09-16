package server

import (
	"strings"
	"testing"

	"github.com/optionalsoftware/ask-about/config"
)

func TestFirstInitial(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "Daniel", "D"},
		{"lowercase is raised", "daniel", "D"},
		{"leading space skipped", "  Daniel", "D"},
		{"accented letter kept", "Ángela", "Á"},
		{"non-latin script", "Дмитрий", "Д"},
		{"digit", "4chan", "4"},
		{"empty", "", ""},
		{"only spaces", "   ", ""},
		// Anything that could open a tag or close an attribute is refused
		// rather than escaped — the whole point of restricting to letters and
		// digits is that the result drops into SVG as-is.
		{"punctuation refused", "<script>", ""},
		{"quote refused", `"`, ""},
		{"emoji refused", "🙂", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstInitial(tc.in); got != tc.want {
				t.Errorf("firstInitial(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildFavicon(t *testing.T) {
	tests := []struct {
		name    string
		subject config.Subject
		want    string
	}{
		{"first name", config.Subject{FirstName: "Daniel", LastName: "Reyes"}, ">D<"},
		{"falls back to surname", config.Subject{LastName: "Reyes"}, ">R<"},
		{"product name", config.Subject{Kind: config.KindProduct, Name: "Larkspur Desk"}, ">L<"},
		{"nothing configured", config.Subject{}, ">?<"},
		{"nothing usable", config.Subject{FirstName: "!!!"}, ">?<"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(buildFavicon(tc.subject))
			if !strings.Contains(got, tc.want) {
				t.Errorf("favicon for %+v does not contain %q:\n%s", tc.subject, tc.want, got)
			}
			if !strings.HasPrefix(got, "<svg") {
				t.Errorf("not an svg:\n%s", got)
			}
		})
	}
}
