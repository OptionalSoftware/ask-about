package pipeline

import (
	"slices"
	"strings"
	"testing"
)

// feed streams text one rune at a time, the way tokens actually arrive, and
// returns every segment the splitter released including the final flush.
func feed(text string) []Segment {
	var s Splitter
	var got []Segment
	for _, r := range text {
		got = append(got, s.Write(string(r))...)
	}
	if tail, ok := s.Flush(); ok {
		got = append(got, tail)
	}
	return got
}

// texts pulls just the sentence text, for cases where separators don't matter.
func texts(segs []Segment) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Text)
	}
	return out
}

// rejoin reconstructs the answer the way the client does, so a test failure
// here means the user would see mangled text.
func rejoin(segs []Segment) string {
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteString(s.Sep)
		}
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestSplitter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain sentences",
			in:   "He led engineering. He reported to the board.",
			want: []string{"He led engineering.", "He reported to the board."},
		},
		{
			name: "question and exclamation",
			in:   "Did the launch slip? No! Never.",
			want: []string{"Did the launch slip?", "No!", "Never."},
		},
		{
			name: "decimals are not boundaries",
			in:   "Revenue grew 4.2x to $1.5M in one year.",
			want: []string{"Revenue grew 4.2x to $1.5M in one year."},
		},
		{
			name: "abbreviations are not boundaries",
			in:   "He worked with Dr. Smith at Acme Inc. on the platform.",
			want: []string{"He worked with Dr. Smith at Acme Inc. on the platform."},
		},
		{
			name: "single-letter initials are not boundaries",
			in:   "J. Reyes led the org.",
			want: []string{"J. Reyes led the org."},
		},
		{
			name: "unterminated text is flushed",
			in:   "This has no terminator",
			want: []string{"This has no terminator"},
		},
		{
			name: "empty input yields nothing",
			in:   "",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := texts(feed(tt.in))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// Identifiers containing a period must survive. Splitting a dotted name like
// "Acme.io" and re-joining it yields "Acme. io" — the bug this guards.
func TestSplitterKeepsDottedIdentifiersIntact(t *testing.T) {
	cases := []string{
		"She was Director of Engineering at Acme.io in Portland.",
		"He codes in Node.js and Go.",
		"The team shipped React.js and .NET services.",
		"They pinned v1.0.3 before the release.",
		"Reach the docs at example.com/careers today.",
	}

	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := rejoin(feed(in)); got != in {
				t.Errorf("text was mangled\n got  %q\n want %q", got, in)
			}
		})
	}
}

// Newlines are structure, not decoration: a list must not collapse into a
// paragraph when the client reassembles it.
func TestSplitterPreservesLineStructure(t *testing.T) {
	const in = "The team owned three areas:\n- Billing.\n- Search.\n- Onboarding.\n"

	segs := feed(in)
	if got, want := len(segs), 4; got != want {
		t.Fatalf("got %d segments, want %d: %q", got, want, texts(segs))
	}
	if got := rejoin(segs); got != strings.TrimRight(in, "\n") {
		t.Errorf("line structure lost\n got  %q\n want %q", got, strings.TrimRight(in, "\n"))
	}
	for _, seg := range segs[1:] {
		if !strings.Contains(seg.Sep, "\n") {
			t.Errorf("segment %q lost its newline separator (got %q)", seg.Text, seg.Sep)
		}
	}
}

func TestSplitterSeparatesParagraphs(t *testing.T) {
	segs := feed("Experience\n\nHe joined in 2021.")
	if got, want := texts(segs), []string{"Experience", "He joined in 2021."}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if segs[1].Sep != "\n\n" {
		t.Errorf("paragraph break lost: separator is %q", segs[1].Sep)
	}
}

// A long run with no terminator must not stall the stream forever.
func TestSplitterFlushesLongRuns(t *testing.T) {
	long := strings.Repeat("word ", 200) // ~1000 chars, no punctuation
	segs := feed(long)
	if len(segs) < 2 {
		t.Fatalf("expected the buffer cap to force multiple releases, got %d", len(segs))
	}
	if got, want := rejoin(segs), strings.TrimSpace(long); got != want {
		t.Errorf("text was lost or reordered across forced splits")
	}
}

// Sentences must survive being split across arbitrary chunk boundaries, since
// a token can land anywhere — including between a period and the next space.
func TestSplitterIsChunkBoundaryAgnostic(t *testing.T) {
	const text = "The org grew at Acme.io.\n- Hiring improved.\n- Delivery sped up."
	want := feed(text)

	for size := 1; size <= 12; size++ {
		var s Splitter
		var got []Segment
		for i := 0; i < len(text); i += size {
			end := min(i+size, len(text))
			got = append(got, s.Write(text[i:end])...)
		}
		if tail, ok := s.Flush(); ok {
			got = append(got, tail)
		}
		if !slices.Equal(got, want) {
			t.Errorf("chunk size %d:\n got  %+v\n want %+v", size, got, want)
		}
	}
}

func TestStripMarkdown(t *testing.T) {
	tests := []struct{ in, want string }{
		{"**Billing** — invoices and payments.", "Billing — invoices and payments."},
		{"## Experience", "Experience"},
		{"He used `kubectl` daily.", "He used kubectl daily."},
		{"__Emphasis__ removed.", "Emphasis removed."},
		{"- Plain list items survive.", "- Plain list items survive."},
		{"He writes C# and ranked #1.", "He writes C# and ranked #1."},
		{"Nothing to strip here.", "Nothing to strip here."},
	}

	for _, tt := range tests {
		if got := stripMarkdown(tt.in); got != tt.want {
			t.Errorf("stripMarkdown(%q)\n got  %q\n want %q", tt.in, got, tt.want)
		}
	}
}
