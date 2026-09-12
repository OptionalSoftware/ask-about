// Package subject is the name and pronouns of whatever the assistant is about,
// and how those are substituted into the persona and the page copy.
//
// It is shared by config, which parses the setting, and corpus, which renders
// the persona — so neither has to know about the other, and the placeholder
// list exists exactly once.
package subject

import (
	"fmt"
	"strings"
	"unicode"
)

// Pronouns is how the persona refers to the subject.
//
// All five forms are held separately because none of them can be derived from
// the others: "her" is both object and possessive, "his" is both possessive
// forms, and knowing "they" tells you nothing about "themselves". A persona
// that guessed would misgender the subject in whichever sentence it got wrong.
type Pronouns struct {
	// Subject is the nominative form: "he led the team".
	Subject string
	// Object is the accusative form: "ask him".
	Object string
	// Possessive is the determiner, which needs a noun after it: "their work".
	Possessive string
	// Independent is the possessive that stands alone: "that decision was hers".
	Independent string
	// Reflexive turns the sentence back on the subject: "he paged himself".
	Reflexive string
}

// The named sets. TheyThem is the default for a person: the cost of the wrong
// default is that a real person is misgendered in every answer their bot
// gives, and this is the only choice that is merely impersonal rather than
// wrong. ItIts is the default for a product, a service, a company — anything
// that is not a person.
var (
	HeHim    = Pronouns{"he", "him", "his", "his", "himself"}
	SheHer   = Pronouns{"she", "her", "her", "hers", "herself"}
	TheyThem = Pronouns{"they", "them", "their", "theirs", "themselves"}
	ItIts    = Pronouns{"it", "it", "its", "its", "itself"}
)

// named are the only sets the pronouns setting accepts.
var named = map[string]Pronouns{
	"he/him": HeHim, "she/her": SheHer, "they/them": TheyThem, "it/its": ItIts,
}

// Default is the set an unset pronouns setting resolves to.
func Default(product bool) Pronouns {
	if product {
		return ItIts
	}
	return TheyThem
}

// Parse reads the pronouns setting. Empty means they/them; otherwise it must
// be one of the four named sets.
func Parse(s string) (Pronouns, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return TheyThem, nil
	}
	// Spaces around the slash are tolerated, so "he / him" is he/him.
	parts := strings.Split(s, "/")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if p, ok := named[strings.Join(parts, "/")]; ok {
		return p, nil
	}
	return Pronouns{}, fmt.Errorf("subject.pronouns %q: want he/him, she/her, they/them, or it/its", s)
}

// IsZero reports that no form is set. Complete reports that every form is.
// Anything in between is a caller that filled in some fields and forgot the
// rest, which a persona must not render: "he" in one sentence and "them" in
// the next reads as though it were describing two people.
func (p Pronouns) IsZero() bool { return p == Pronouns{} }

func (p Pronouns) Complete() bool {
	return p.Subject != "" && p.Object != "" && p.Possessive != "" &&
		p.Independent != "" && p.Reflexive != ""
}

// Replacer expands the placeholders the persona and the page copy may use:
// the three name forms, the five pronoun forms, and each pronoun capitalised
// for the start of a sentence — a persona that wrote "{{they}} shipped it"
// sentence-initially would otherwise render "he shipped it" in lower case.
func Replacer(first, last, full string, p Pronouns) *strings.Replacer {
	return strings.NewReplacer(
		"{{firstName}}", first,
		"{{lastName}}", last,
		"{{fullName}}", full,
		"{{they}}", p.Subject,
		"{{them}}", p.Object,
		"{{their}}", p.Possessive,
		"{{theirs}}", p.Independent,
		"{{themselves}}", p.Reflexive,
		"{{They}}", upperFirst(p.Subject),
		"{{Them}}", upperFirst(p.Object),
		"{{Their}}", upperFirst(p.Possessive),
		"{{Theirs}}", upperFirst(p.Independent),
		"{{Themselves}}", upperFirst(p.Reflexive),
	)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}
