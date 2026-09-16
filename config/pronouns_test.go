package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/optionalsoftware/ask-about/subject"
)

// A bad value has to stop the process. A persona that refers to its subject as
// "" is worse than a refusal to start.
func TestBadPronounsFailValidation(t *testing.T) {
	// Establish that the defaults validate on their own, or the assertion below
	// would pass on whatever unrelated guard fired first.
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults do not validate, so this test proves nothing: %v", err)
	}

	cfg.Subject.Pronouns = "xe/xem"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("validate accepted an incomplete pronoun set")
	}
	if !strings.Contains(err.Error(), "subject.pronouns") {
		t.Errorf("the error does not name the setting at fault: %v", err)
	}
}

// Configured copy gets the pronouns too, so a disclaimer or a no-link message
// is as portable as the persona.
func TestCopySubstitutesPronouns(t *testing.T) {
	s := Subject{FirstName: "Dana", LastName: "Reed", Pronouns: "she/her",
		Disclaimer: "Ask {{firstName}} about {{their}} work. {{They}} answers here."}
	if got, want := s.DisclaimerText(),
		"Ask Dana about her work. She answers here."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// PronounSet is called while rendering pages, where there is no error to
// return, so a value validate would have rejected has to degrade to the default
// rather than produce empty pronouns.
func TestPronounSetFallsBackOnBadValue(t *testing.T) {
	s := Subject{FirstName: "Dana", Pronouns: "xe/xem"}
	if got := s.PronounSet(); got != subject.TheyThem {
		t.Errorf("got %+v, want the default %+v", got, subject.TheyThem)
	}
	// And nothing renders as an empty string, which is the failure this guards.
	s.Disclaimer = "Ask {{firstName}} about {{their}} work."
	if got, want := s.DisclaimerText(), "Ask Dana about their work."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The no-link message shares the replacer with the disclaimer, so it gets the
// pronouns too — it is the one page a stranger sees.
func TestNoLinkMessageSubstitutesPronouns(t *testing.T) {
	s := Subject{FirstName: "Dana", LastName: "Reed", Pronouns: "she/her"}
	a := Access{NoLink: "{{firstName}} hands these out {{themselves}}. Ask {{them}}."}
	if got, want := a.NoLinkMessage(s),
		"Dana hands these out herself. Ask her."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The shipped defaults are copy nobody edits before first run, which is how
// "Ask about roles, scope, or how he works" survived in here. corpus has the
// same guard over the persona; this one covers everything config ships.
func TestShippedDefaultCopyNamesNoGender(t *testing.T) {
	d := Default()
	// Default sets no Questions, so StarterQuestions here returns the generic
	// fallbacks — which are the ones that actually ship.
	texts := append([]string{
		d.Subject.Tagline,
		defaultDisclaimer,
		defaultNoLinkMessage,
		defaultAccessMessage,
		defaultSessionCapMessage,
		defaultDayCapMessage,
	}, d.Subject.StarterQuestions()...)

	gendered := regexp.MustCompile(`(?i)\b(he|him|his|himself|she|her|hers|herself)\b`)
	for _, text := range texts {
		if w := gendered.FindString(text); w != "" {
			t.Errorf("shipped copy says %q: %q", w, text)
		}
	}
}

// A product defaults to it/its, a person to they/them, and an explicit set
// wins for either — a company that calls itself "we" in its own copy may well
// want they/them.
func TestPronounDefaultFollowsKind(t *testing.T) {
	if got := (Subject{Kind: KindProduct}).PronounSet(); got != subject.ItIts {
		t.Errorf("product default = %+v, want it/its", got)
	}
	if got := (Subject{}).PronounSet(); got != subject.TheyThem {
		t.Errorf("person default = %+v, want they/them", got)
	}
	if got := (Subject{Kind: KindProduct, Pronouns: "they/them"}).PronounSet(); got != subject.TheyThem {
		t.Errorf("explicit pronouns on a product were ignored: %+v", got)
	}
}

// A product has one name. It fills every name placeholder so copy written for
// a person still reads, and it is what the page and the favicon use.
func TestProductNameFillsEveryNamePlaceholder(t *testing.T) {
	s := Subject{Kind: KindProduct, Name: "Larkspur Desk",
		Disclaimer: "[{{firstName}}] [{{lastName}}] [{{fullName}}] is {{their}} own {{they}}."}
	if got, want := s.FullName(), "Larkspur Desk"; got != want {
		t.Errorf("FullName = %q, want %q", got, want)
	}
	if got, want := s.DisclaimerText(), "[Larkspur Desk] [] [Larkspur Desk] is its own it."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Name wins over first/last if someone sets both.
	s = Subject{Name: "Larkspur Desk", FirstName: "Daniel", LastName: "Reyes"}
	if got := s.FullName(); got != "Larkspur Desk" {
		t.Errorf("FullName with both = %q, want the product name", got)
	}
}

func TestKindIsValidated(t *testing.T) {
	cfg := Default()
	cfg.Subject.Kind = "service"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "subject.kind") {
		t.Errorf("an unknown kind was accepted: %v", err)
	}
	cfg = Default()
	cfg.Subject.Kind = KindProduct
	cfg.Subject.Name = "Larkspur Desk"
	if err := cfg.Validate(); err != nil {
		t.Errorf("a valid product config was refused: %v", err)
	}
	// "company" is what people write for a company; it is a product to the code.
	cfg.Subject.Kind = KindCompany
	if err := cfg.Validate(); err != nil {
		t.Errorf("kind = company was refused: %v", err)
	}
	if !cfg.Subject.IsProduct() || cfg.Subject.PersonaFile() != "prompts/product.md" {
		t.Error("kind = company does not behave as a product")
	}
	// A subject with no name at all renders "Ask about " on the page.
	cfg = Default()
	cfg.Subject.FirstName, cfg.Subject.LastName = "", ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("a nameless subject was accepted: %v", err)
	}
	// A surname alone renders blanks wherever the persona says {{firstName}}.
	cfg = Default()
	cfg.Subject.FirstName = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "firstName") {
		t.Errorf("a person with only a surname was accepted: %v", err)
	}
	// A product has no first name and must not be caught by that rule.
	cfg = Default()
	cfg.Subject.Kind, cfg.Subject.Name = KindProduct, "Larkspur Desk"
	cfg.Subject.FirstName, cfg.Subject.LastName = "", ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("a product was refused for lacking a first name: %v", err)
	}
}

// The persona follows the kind unless a path is set, and a set path wins for
// either kind — someone with their own persona should not lose it by
// switching kind.
func TestPersonaPathFollowsKind(t *testing.T) {
	cfg := Default()
	if got := cfg.PersonaPath(); got != "prompts/person.md" {
		t.Errorf("person persona = %q", got)
	}
	cfg.Subject.Kind = KindProduct
	if got := cfg.PersonaPath(); got != "prompts/product.md" {
		t.Errorf("product persona = %q", got)
	}
	cfg.Corpus.Persona = "private/persona.md"
	if got := cfg.PersonaPath(); got != "private/persona.md" {
		t.Errorf("explicit persona path was overridden: %q", got)
	}
}

// The front-door message for a product should not tell a stranger to get in
// touch with the product.
func TestNoLinkMessageDefaultFollowsKind(t *testing.T) {
	person := Access{}.NoLinkMessage(Subject{FirstName: "Dana", LastName: "Reed"})
	product := Access{}.NoLinkMessage(Subject{Kind: KindProduct, Name: "Larkspur Desk"})
	if !strings.Contains(person, "Get in touch with Dana") {
		t.Errorf("person default = %q", person)
	}
	if !strings.Contains(product, "the people at Larkspur Desk") {
		t.Errorf("product default = %q", product)
	}
}

// A file with its own [subject] gets only the names it sets. Inheriting the
// built-in subject's surname because the file set just a first name is the
// kind of thing nobody notices until a visitor asks who "Dana Reyes" is.
func TestPartialSubjectDoesNotInheritDefaultNames(t *testing.T) {
	write := func(body string) Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return cfg
	}
	base := "[llm]\nvendor = \"anthropic\"\nmodel = \"m\"\n"

	if got := write(base + "[subject]\nfirstName = \"Dana\"\n").Subject.FullName(); got != "Dana" {
		t.Errorf("first name only = %q, want just Dana", got)
	}
	cfg := write(base + "[subject]\nkind = \"product\"\nname = \"Larkspur Desk\"\n")
	if first, last, full := cfg.Subject.NameParts(); first != "Larkspur Desk" || last != "" || full != "Larkspur Desk" {
		t.Errorf("product name parts = %q %q %q", first, last, full)
	}
	// No [subject] at all keeps the built-in one, so a bare file still runs.
	if got := write(base).Subject.FullName(); got != Default().Subject.FullName() {
		t.Errorf("no [subject] = %q, want the default", got)
	}
	// A [subject] that names nobody is refused rather than rendered blank.
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(path, []byte(base+"[subject]\ntagline = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("nameless [subject] was accepted: %v", err)
	}
}

// The two hardening settings are checked at load, so a typo is a startup
// error rather than a proxy that is silently never trusted or a window that
// never prunes.
func TestProxyAndRetentionAreValidated(t *testing.T) {
	cfg := Default()
	cfg.Server.TrustedProxy = "not-an-address"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.trusted_proxy") {
		t.Errorf("bad trusted_proxy accepted: %v", err)
	}
	for _, ok := range []string{"", "127.0.0.1", "10.0.0.0/8", "::1"} {
		cfg = Default()
		cfg.Server.TrustedProxy = ok
		if err := cfg.Validate(); err != nil {
			t.Errorf("trusted_proxy %q refused: %v", ok, err)
		}
	}
	cfg = Default()
	cfg.Storage.RetainDays = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "retain_days") {
		t.Errorf("negative retain_days accepted: %v", err)
	}
}
