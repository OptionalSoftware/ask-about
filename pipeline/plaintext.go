package pipeline

import "strings"

// stripMarkdown flattens formatting for consumers that can't render it.
//
// The browser renders markdown; a speech synthesizer cannot — "**Billing**"
// is either read aloud as asterisks or loses its emphasis. So the pipeline
// emits the model's text verbatim and each consumer adapts: the UI renders,
// the TTS adapter calls this first.
//
// It is deliberately narrow — emphasis markers, backticks, and heading hashes
// only. List dashes and newlines survive, because TTS reads them fine as
// pauses and they carry real structure.
func stripMarkdown(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")

	// Leading heading hashes ("## Experience" -> "Experience"). Only at the
	// start, so "C#" and "#1" are untouched.
	trimmed := strings.TrimLeft(s, " \t")
	if strings.HasPrefix(trimmed, "#") {
		hashes := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		if hashes <= 6 && strings.HasPrefix(trimmed[hashes:], " ") {
			s = strings.TrimSpace(trimmed[hashes:])
		}
	}

	return s
}
