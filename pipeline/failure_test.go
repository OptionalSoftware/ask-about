package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/optionalsoftware/ask-about/llm"
)

// failingProvider stands in for the vendor, so every failure below is exercised
// without a network call or a cent of spend.
type failingProvider struct {
	// openErr fails before the stream starts, as an HTTP error would.
	openErr error
	// midErr arrives on the channel after some text, as a dropped connection
	// or a mid-stream overload would.
	midErr error
	text   string
}

func (failingProvider) Name() string { return "mock" }

func (f failingProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	out := make(chan llm.Chunk, 2)
	if f.text != "" {
		out <- llm.Chunk{Text: f.text}
	}
	out <- llm.Chunk{Err: f.midErr}
	close(out)
	return out, nil
}

func drain(t *testing.T, p *Pipeline) []Event {
	t.Helper()
	var got []Event
	for ev := range p.Run(t.Context(), llm.Request{Messages: []llm.Message{{Text: "hi"}}}) {
		got = append(got, ev)
	}
	return got
}

func quietPipeline(provider llm.Provider) *Pipeline {
	return New(provider, PassThrough{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The visitor is a stranger reading about someone's career. A vendor error
// string tells them nothing they can act on and leaks the endpoint, the status
// code and which vendor is in use.
func TestVendorErrorsAreNotShownToVisitors(t *testing.T) {
	raw := errors.New(`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests ` +
		`{"type":"error","error":{"type":"rate_limit_error","message":"..."}}`)

	for _, tc := range []struct {
		name     string
		provider failingProvider
	}{
		{"fails before streaming", failingProvider{openErr: raw}},
		{"fails mid-stream", failingProvider{text: "A partial answer. ", midErr: raw}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errText string
			for _, ev := range drain(t, quietPipeline(tc.provider)) {
				if ev.Kind == EventError {
					errText = ev.Text
				}
			}
			if errText == "" {
				t.Fatal("no error event reached the client")
			}
			for _, leak := range []string{
				"anthropic.com", "429", "rate_limit_error", "POST", "https://",
			} {
				if strings.Contains(errText, leak) {
					t.Errorf("error shown to the visitor leaks %q: %s", leak, errText)
				}
			}
			if errText != failBusy {
				t.Errorf("message = %q, want the busy message", errText)
			}
		})
	}
}

func TestVisitorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rate limited", errors.New("429 Too Many Requests"), failBusy},
		{"overloaded", errors.New("Error: overloaded_error"), failBusy},
		{"rate limit phrasing", errors.New("rate limit exceeded"), failBusy},
		{"server error", errors.New("500 Internal Server Error"), failUpstream},
		{"bad gateway", errors.New("502 Bad Gateway"), failUpstream},
		{"unavailable", errors.New("503 Service Unavailable"), failUpstream},
		{"timeout text", errors.New("context deadline exceeded"), failUpstream},
		{"timeout sentinel", context.DeadlineExceeded, failUpstream},
		{"wrapped timeout", fmt.Errorf("stream: %w", context.DeadlineExceeded), failUpstream},
		{"connection refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, failUpstream},
		{"cancelled", context.Canceled, failCancelled},
		// A bad key is the operator's problem. Naming it would tell whoever is
		// probing exactly what is wrong.
		{"bad api key", errors.New("401 authentication_error: invalid x-api-key"), failGeneric},
		{"unknown", errors.New("something odd happened"), failGeneric},
		{"nil", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := visitorMessage(tc.err); got != tc.want {
				t.Errorf("visitorMessage(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// Every message a visitor can see must be plain English, since these are the
// only words a stranger gets when something breaks.
func TestFailureMessagesAreReadable(t *testing.T) {
	for _, m := range []string{failBusy, failUpstream, failCancelled, failGeneric} {
		if m == "" {
			t.Error("a failure message is empty")
		}
		for _, jargon := range []string{"error", "http", "api", "500", "429", "null"} {
			if strings.Contains(strings.ToLower(m), jargon) {
				t.Errorf("%q contains %q", m, jargon)
			}
		}
		if !strings.HasSuffix(m, ".") {
			t.Errorf("%q is not a sentence", m)
		}
	}
}

// A failed turn is still recorded: it cost tokens, and what someone asked is
// worth keeping even when the answer never arrived.
func TestFailedTurnIsStillObserved(t *testing.T) {
	var seen Result
	p := quietPipeline(failingProvider{openErr: fmt.Errorf("503 Service Unavailable")})
	p.Observe(observerFunc(func(_ context.Context, _ llm.Request, r Result) { seen = r }))

	drain(t, p)

	if seen.Err == nil {
		t.Error("a failed turn was not reported to the observer")
	}
	// The observer gets the real error, not the visitor's version.
	if !strings.Contains(seen.Err.Error(), "503") {
		t.Errorf("observer lost the underlying error: %v", seen.Err)
	}
}

type observerFunc func(context.Context, llm.Request, Result)

func (f observerFunc) RunFinished(ctx context.Context, req llm.Request, r Result) {
	f(ctx, req, r)
}

// Recording is detached from the request so a disconnect cannot cancel it, but
// it still has to end. Without a deadline a store that hangs holds this
// goroutine for the life of the process.
func TestObserverGetsADeadlineButNotTheRequestsCancellation(t *testing.T) {
	// Read inside the call: observe cancels its context on return, so
	// inspecting it afterwards says nothing about what the observer saw.
	var ranWith error
	var deadline time.Time
	var hadDeadline bool
	p := quietPipeline(failingProvider{openErr: errors.New("boom")})
	p.Observe(observerFunc(func(ctx context.Context, _ llm.Request, _ Result) {
		ranWith = ctx.Err()
		deadline, hadDeadline = ctx.Deadline()
	}))

	// A request context that is already cancelled, as a disconnect leaves it.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range p.Run(ctx, llm.Request{Messages: []llm.Message{{Text: "hi"}}}) {
	}

	if ranWith != nil {
		t.Errorf("the observer inherited the request's cancellation: %v", ranWith)
	}
	if !hadDeadline {
		t.Fatal("the observer's context has no deadline — a hung store blocks forever")
	}
	if d := time.Until(deadline); d <= 0 || d > observeTimeout {
		t.Errorf("deadline is %v away, want between 0 and %v", d, observeTimeout)
	}
}
