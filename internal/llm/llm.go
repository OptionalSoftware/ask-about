// Package llm is the vendor-neutral seam between ask-about and whatever model is
// answering. Adding a vendor means implementing Provider and adding one case
// to New — nothing upstream of this package changes.
//
// Stream is the only primitive. Complete is defined in terms of it so the two
// modes cannot drift apart, and so a vendor that has no streaming endpoint
// (Bedrock, today) still satisfies the whole interface by emitting one chunk.
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/optionalsoftware/ask-about/internal/config"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of conversation.
type Message struct {
	Role Role
	Text string
}

// Request is everything a provider needs for one completion.
type Request struct {
	System   string
	Messages []Message
	// Session identifies the visit this turn belongs to. Carried here so it
	// reaches whatever records the turn; no adapter sends it to a vendor.
	Session string
	// Invite identifies the link the visitor arrived on, empty for a turn
	// taken outside one. Same deal — it travels with the request and stops at
	// the recorder.
	Invite string
}

// Usage is the token accounting for one completion.
//
// The four counts are kept separate because they price differently — a cache
// read is a fraction of uncached input, and a cache write costs more than it.
// Collapsing them into one number would make spend estimates wrong in the
// direction that matters.
//
// Raw counts are what get stored; cost is derived from them at read time, so a
// wrong rate in config is fixable retroactively rather than baked into history.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	// Model is what actually served the request, which may differ from what
	// was configured if a vendor routes or falls back.
	Model string `json:"model"`
}

// Total is every token the request touched, for a rough sanity check. It is
// deliberately not used for cost — the components price differently.
func (u Usage) Total() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}

// Chunk is one piece of a streamed response. A non-nil Err is terminal: the
// channel is closed immediately after.
//
// Usage arrives on its own chunk at the end of a successful stream, with no
// Text — vendors only report totals once generation finishes. A stream that
// errors may carry no usage at all.
type Chunk struct {
	Text  string
	Usage *Usage
	Err   error
}

// Provider is a model vendor. Implementations must close the returned channel
// when the response ends, and must stop and close promptly on ctx cancellation.
type Provider interface {
	// Name identifies the provider in logs and errors.
	Name() string

	// Stream sends req and returns response text as it arrives.
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// New builds the provider named in cfg.
func New(cfg config.LLM) (Provider, error) {
	key, err := cfg.APIKey()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.Vendor, err)
	}

	switch cfg.Vendor {
	case "anthropic":
		return newAnthropic(cfg, key), nil
	case "openai":
		return newOpenAI(cfg, key), nil
	case "bedrock":
		return newBedrock(cfg, key), nil
	default:
		return nil, fmt.Errorf("unknown llm vendor %q", cfg.Vendor)
	}
}

// Complete drains a stream into a single string. Callers that don't want
// incremental output use this; there is no separate non-streaming code path.
func Complete(ctx context.Context, p Provider, req Request) (string, error) {
	chunks, err := p.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for c := range chunks {
		if c.Err != nil {
			return b.String(), c.Err
		}
		b.WriteString(c.Text)
	}
	return b.String(), nil
}
