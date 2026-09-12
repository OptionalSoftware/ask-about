package subject

import (
	"strings"
	"testing"
)

func TestNamedSets(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Pronouns
	}{
		{"he/him", HeHim},
		{"she/her", SheHer},
		{"they/them", TheyThem},
		{"it/its", ItIts},
		// Case and surrounding space are what a config file actually contains.
		{"  He/Him  ", HeHim},
		{"he / him", HeHim},
		{" She / Her ", SheHer},
	} {
		got, err := Parse(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// An unset setting must not fall through to one subject's pronouns. they/them
// is impersonal; anything else misgenders somebody.
func TestEmptyIsTheyThem(t *testing.T) {
	got, err := Parse("")
	if err != nil || got != TheyThem {
		t.Errorf("Parse(\"\") = %+v, %v; want they/them", got, err)
	}
	if Default(false) != TheyThem || Default(true) != ItIts {
		t.Error("Default does not follow the kind")
	}
}

// Only the four named sets are accepted; nothing is spelled out or guessed.
func TestOnlyNamedSetsAreAccepted(t *testing.T) {
	for _, in := range []string{
		"xe/xem", "xe/xem/xyr/xyrs/xemself", "he/him/his/his/himself",
		"he/they", "she/they", "him/he", "////", "nonsense",
	} {
		if got, err := Parse(in); err == nil {
			t.Errorf("%q was accepted as %+v", in, got)
		}
	}
}

func TestCompleteAndZero(t *testing.T) {
	if !(Pronouns{}).IsZero() || (Pronouns{}).Complete() {
		t.Error("the zero set is zero and not complete")
	}
	if !HeHim.Complete() || HeHim.IsZero() {
		t.Error("a named set is complete and not zero")
	}
	if p := (Pronouns{Subject: "he"}); p.IsZero() || p.Complete() {
		t.Error("a partial set is neither zero nor complete")
	}
}

// Every placeholder, lower case and sentence-initial. A misspelt one is
// invisible in review and ships literal braces to the model.
func TestReplacerExpandsEveryPlaceholder(t *testing.T) {
	const every = "{{firstName}} {{lastName}} {{fullName}} " +
		"{{they}} {{them}} {{their}} {{theirs}} {{themselves}} " +
		"{{They}} {{Them}} {{Their}} {{Theirs}} {{Themselves}}"
	got := Replacer("Dana", "Reed", "Dana Reed", SheHer).Replace(every)
	want := "Dana Reed Dana Reed she her her hers herself She Her Her Hers Herself"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "{{") {
		t.Errorf("a placeholder survived: %q", got)
	}
}
