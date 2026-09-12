// Package corpus loads the source document the bot answers from and assembles
// it into a system prompt.
//
// The corpus is small enough (~14k tokens) to sit in every request, so there is
// no retrieval step: the model sees the whole document every turn and the
// prompt cache absorbs the cost. Revisit if the corpus outgrows ~50k tokens.
package corpus

import (
	"fmt"
	"os"
	"strings"

	"github.com/optionalsoftware/ask-about/internal/subject"
)

type Corpus struct {
	Persona string
	Content string
	system  string
}

// Subject is what the corpus describes — a person or a product. Its name and
// pronouns are substituted into the persona rather than written into it, so
// one persona serves any corpus.
type Subject struct {
	First string
	Last  string
	Full  string
	// Pronouns may be left entirely zero, in which case they/them is used. A
	// partly filled set is an error rather than a mix.
	Pronouns subject.Pronouns
}

// Load reads the persona and content files, falling back to the supplied
// embedded copies when a path is missing. Disk always wins, so pointing the
// binary at a different corpus needs no rebuild.
func Load(personaPath, contentPath string, embeddedPersona, embeddedContent string, subj Subject) (*Corpus, error) {
	persona, err := readOr(personaPath, embeddedPersona)
	if err != nil {
		return nil, fmt.Errorf("persona: %w", err)
	}
	p := subj.Pronouns
	switch {
	case p.IsZero():
		p = subject.Default(false)
	case !p.Complete():
		return nil, fmt.Errorf("pronouns: %+v is missing a form; pass all five or none", p)
	}
	persona = subject.Replacer(subj.First, subj.Last, subj.Full, p).Replace(persona)
	content, err := readOr(contentPath, embeddedContent)
	if err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("corpus is empty (looked at %q)", contentPath)
	}

	c := &Corpus{Persona: persona, Content: content}
	c.system = buildSystem(persona, content)
	return c, nil
}

// System returns the assembled system prompt. It is stable for the lifetime of
// the process, which is what makes it cacheable.
func (c *Corpus) System() string { return c.system }

func buildSystem(persona, content string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(persona))
	b.WriteString("\n\n---\n\n")
	// Called "records" rather than "document" throughout, including the tag
	// name. The model mirrors the vocabulary it is given, and a prompt that
	// says "document" a dozen times will produce answers that say "the
	// document" no matter how firmly the persona forbids it.
	b.WriteString("These are your records — the only source of truth available ")
	b.WriteString("to you. Answer strictly from them.\n\n")
	b.WriteString("<records>\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n</records>\n")
	return b.String()
}

func readOr(path, fallback string) (string, error) {
	if path == "" {
		return fallback, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
