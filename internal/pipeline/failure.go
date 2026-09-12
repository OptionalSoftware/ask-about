package pipeline

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Failure messages shown to a visitor when a turn cannot be completed.
//
// A vendor's error string is written for whoever holds the API key, not for a
// stranger reading a page about someone's career. Left unmapped it renders as
// "POST https://api.anthropic.com/v1/messages: 429 Too Many Requests {...}",
// which leaks the vendor, the endpoint and the shape of the failure, and reads
// as broken rather than busy.
//
// The real error still goes to the log and to the stored turn, so nothing is
// lost for debugging.
const (
	// Both name the provider rather than saying "something went wrong". The
	// distinction matters to the reader: this is not the site being broken or
	// the question being bad, and there is nothing for them to fix.
	failBusy = "The underlying LLM provider is rate limiting us. " +
		"Give it a few seconds and ask again."
	failUpstream = "The underlying LLM provider is experiencing issues. " +
		"We are unable to complete your request right now."
	failCancelled = "That question was cancelled."
	failGeneric   = "Something went wrong answering that. Try again."
)

// visitorMessage maps an error to something worth showing a stranger.
//
// Matching is on substrings of the error text because the three adapters
// surface failures differently: the Anthropic and OpenAI SDKs wrap typed API
// errors, while the Bedrock adapter builds its own from a status code. A
// shared type to assert on would have to be threaded through all three, and
// the classification here only needs to pick one of four sentences.
func visitorMessage(err error) string {
	if err == nil {
		return ""
	}
	// A disconnect or a client-side abort is not a fault worth reporting; the
	// browser has usually gone by the time this is written anyway.
	if errors.Is(err, context.Canceled) {
		return failCancelled
	}

	// No response at all: DNS, refused connection, dropped socket. From the
	// visitor's side this is the same as the vendor being down.
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return failUpstream
	}

	switch text := strings.ToLower(err.Error()); {
	case strings.Contains(text, "429"),
		strings.Contains(text, "rate limit"),
		strings.Contains(text, "overloaded"),
		strings.Contains(text, "too many requests"):
		// Retries are exhausted by the time this is reached, so the honest
		// advice is to wait rather than to retry immediately.
		return failBusy
	case strings.Contains(text, "500"), strings.Contains(text, "502"),
		strings.Contains(text, "503"), strings.Contains(text, "504"),
		strings.Contains(text, "internal server error"),
		strings.Contains(text, "bad gateway"),
		strings.Contains(text, "unavailable"),
		strings.Contains(text, "timeout"), strings.Contains(text, "timed out"),
		// A wrapped vendor error can carry this text without wrapping the
		// sentinel, so the errors.Is check above does not always catch it.
		strings.Contains(text, "deadline exceeded"):
		return failUpstream
	case strings.Contains(text, "401"), strings.Contains(text, "403"),
		strings.Contains(text, "authentication"), strings.Contains(text, "api key"):
		// A bad key is the operator's problem, and saying so to a visitor
		// would advertise exactly what is wrong to whoever is looking.
		return failGeneric
	default:
		return failGeneric
	}
}
