package pipeline

import (
	"strings"
	"unicode"
)

// maxBuffer caps how much text accumulates before the splitter gives up
// looking for a boundary and emits anyway. Without it, a long stretch with no
// terminator (a code block, a run-on list) would stall the stream.
const maxBuffer = 400

// abbreviations that end in a period but do not end a sentence. Kept short on
// purpose — the cost of a wrong split is a slightly early release, not a bug.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
	"sr": true, "jr": true, "st": true, "vs": true, "etc": true,
	"inc": true, "ltd": true, "co": true, "corp": true, "dept": true,
	"e.g": true, "i.e": true, "approx": true, "est": true, "no": true,
}

// Segment is one released unit of text plus the whitespace that preceded it.
//
// Carrying the separator matters: "Acme.io" and a bulleted list are both
// destroyed if the client re-joins segments with a space of its own choosing.
// The splitter saw the original whitespace, so it reports it rather than
// leaving the client to guess.
type Segment struct {
	Text string
	Sep  string
}

// Splitter turns a token stream into complete sentences.
//
// It exists for two reasons that happen to want the same thing: TTS needs
// whole sentences to synthesize natural prosody, and the guard stage needs a
// complete unit of meaning to judge. One splitter serves both.
type Splitter struct {
	buf strings.Builder // the sentence being accumulated
	sep strings.Builder // whitespace seen since the last emit
	// pending records that we just wrote a terminator and are waiting to see
	// whether whitespace follows. Without this one-rune lookahead, "Acme.io"
	// and "Node.js" split mid-identifier.
	pending bool
}

// Write feeds the next piece of model output and returns any segments that
// completed. The returned slice may be empty.
func (s *Splitter) Write(text string) []Segment {
	var done []Segment

	for _, r := range text {
		// A terminator only ends a sentence when whitespace follows it.
		if s.pending {
			s.pending = false
			if unicode.IsSpace(r) {
				done = s.emit(done)
				s.sep.WriteRune(r)
				continue
			}
			// Not a boundary after all — "Acme.io", "3.5x", "v1.0".
		}

		// A newline ends the current line: list items and headings are
		// complete units even without terminal punctuation.
		if r == '\n' && s.buf.Len() > 0 {
			done = s.emit(done)
			s.sep.WriteRune(r)
			continue
		}

		// No terminator in sight and the buffer is getting long — break at the
		// next space so the stream doesn't stall. The space itself becomes the
		// separator, never part of either segment.
		if r == ' ' && s.buf.Len() >= maxBuffer {
			done = s.emit(done)
			s.sep.WriteRune(r)
			continue
		}

		// Whitespace between segments belongs to the next segment's Sep.
		if s.buf.Len() == 0 && unicode.IsSpace(r) {
			s.sep.WriteRune(r)
			continue
		}

		s.buf.WriteRune(r)

		if r == '.' || r == '!' || r == '?' {
			s.pending = !(r == '.' && (endsInAbbreviation(s.buf.String()) || endsInNumber(s.buf.String())))
		}
	}

	return done
}

// Flush returns whatever is left in the buffer and clears it. Call it once the
// model stops, so a response with no trailing punctuation still gets delivered.
func (s *Splitter) Flush() (Segment, bool) {
	if strings.TrimSpace(s.buf.String()) == "" {
		s.buf.Reset()
		s.sep.Reset()
		return Segment{}, false
	}
	seg := Segment{Text: strings.TrimRight(s.buf.String(), " \t"), Sep: s.sep.String()}
	s.buf.Reset()
	s.sep.Reset()
	s.pending = false
	return seg, true
}

// emit appends the buffered sentence, if any, and resets for the next one.
func (s *Splitter) emit(done []Segment) []Segment {
	text := strings.TrimRight(s.buf.String(), " \t")
	s.buf.Reset()
	if text == "" {
		// Nothing to release; keep accumulating separator whitespace.
		return done
	}
	seg := Segment{Text: text, Sep: s.sep.String()}
	s.sep.Reset()
	return append(done, seg)
}

// endsInAbbreviation reports whether the text ending at a period is a known
// abbreviation or a single-letter initial ("J." in "J. Reyes").
func endsInAbbreviation(text string) bool {
	trimmed := strings.TrimSuffix(text, ".")
	idx := strings.LastIndexFunc(trimmed, unicode.IsSpace)
	word := strings.ToLower(trimmed[idx+1:])
	if word == "" {
		return false
	}
	if len([]rune(word)) == 1 {
		return true
	}
	return abbreviations[word]
}

// endsInNumber reports whether the period is a decimal point ("$1.5M", "4.2x").
func endsInNumber(text string) bool {
	trimmed := strings.TrimSuffix(text, ".")
	if trimmed == "" {
		return false
	}
	runes := []rune(trimmed)
	return unicode.IsDigit(runes[len(runes)-1])
}
