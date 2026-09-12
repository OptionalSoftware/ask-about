// Package pipeline turns a provider's token stream into vetted sentences.
//
//	provider.Stream -> Splitter -> Guard -> Event channel -> SSE / TTS
//
// Each stage is a plain function of the one before it, so the whole thing is
// testable without a server or a network call.
package pipeline

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/optionalsoftware/ask-about/internal/llm"
)

type EventKind string

const (
	// EventSentence carries one released sentence.
	EventSentence EventKind = "sentence"
	// EventBlocked reports that a sentence was withheld by the guard.
	EventBlocked EventKind = "blocked"
	// EventDone marks the end of a successful response.
	EventDone EventKind = "done"
	// EventError is terminal.
	EventError EventKind = "error"
)

type Event struct {
	Kind EventKind `json:"kind"`
	Text string    `json:"text,omitempty"`
	// Sep is the whitespace that preceded Text in the model's output. The
	// client prepends it verbatim rather than inventing a separator, which is
	// what keeps lists on separate lines instead of collapsing to a paragraph.
	Sep string `json:"sep,omitempty"`
}

type Pipeline struct {
	provider llm.Provider
	guard    Guard
	observer Observer
	log      *slog.Logger
}

func New(provider llm.Provider, guard Guard, log *slog.Logger) *Pipeline {
	if guard == nil {
		guard = PassThrough{}
	}
	return &Pipeline{provider: provider, guard: guard, log: log}
}

// fail reports a failed turn to the visitor in words meant for them, and the
// underlying error to the log. The two audiences want different things: one
// needs to know whether to try again, the other needs the status code.
func (p *Pipeline) fail(ctx context.Context, out chan<- Event, err error) {
	p.log.Error("turn failed", "err", err, "vendor", p.provider.Name())
	p.emit(ctx, out, Event{Kind: EventError, Text: visitorMessage(err)})
}

// Observe registers a sink for completed runs. Nil disables recording,
// which is what the tests and any storage-less deployment use.
func (p *Pipeline) Observe(o Observer) { p.observer = o }

// observeTimeout bounds how long recording a finished turn may take. Generous
// against any real write; the point is that it ends.
const observeTimeout = 10 * time.Second

func (p *Pipeline) observe(ctx context.Context, req llm.Request, r Result) {
	if p.observer == nil {
		return
	}
	// Detached from the request so a client disconnect does not cancel the
	// write — by then the answer is already sent and the turn still cost
	// money. Given its own deadline, though: without one, a store that hangs
	// holds this goroutine forever. SQLite's busy_timeout bounds it today,
	// which is a property of the current backend rather than of this code.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), observeTimeout)
	defer cancel()
	p.observer.RunFinished(ctx, req, r)
}

// Result reports what a completed run produced, for the caller to record.
// It is delivered through the Observer rather than the event channel so the
// browser is never told what a conversation cost.
type Result struct {
	Answer  string
	Usage   llm.Usage
	Latency time.Duration
	Blocked int
	Err     error
}

// Observer is notified once per run, after the stream ends. It is called
// synchronously on the pipeline's goroutine, so an implementation that
// blocks holds the connection open — persist quickly or hand off.
type Observer interface {
	RunFinished(ctx context.Context, req llm.Request, r Result)
}

// Run streams a response, emitting one event per completed sentence. The
// returned channel is closed when the response ends for any reason.
func (p *Pipeline) Run(ctx context.Context, req llm.Request) <-chan Event {
	out := make(chan Event)

	go func() {
		defer close(out)

		started := time.Now()
		var result Result
		var answer strings.Builder
		// Reported even on failure: a run that errored still burned tokens
		// and still tells you something about what people are asking.
		defer func() {
			result.Answer = answer.String()
			result.Latency = time.Since(started)
			p.observe(ctx, req, result)
		}()

		chunks, err := p.provider.Stream(ctx, req)
		if err != nil {
			result.Err = err
			p.fail(ctx, out, err)
			return
		}

		var splitter Splitter
		for chunk := range chunks {
			if chunk.Err != nil {
				result.Err = chunk.Err
				p.fail(ctx, out, chunk.Err)
				return
			}
			if chunk.Usage != nil {
				result.Usage = *chunk.Usage
			}
			for _, seg := range splitter.Write(chunk.Text) {
				answer.WriteString(seg.Sep)
				answer.WriteString(seg.Text)
				if !p.release(ctx, out, seg) {
					return
				}
			}
		}

		if tail, ok := splitter.Flush(); ok {
			answer.WriteString(tail.Sep)
			answer.WriteString(tail.Text)
			if !p.release(ctx, out, tail) {
				return
			}
		}
		p.emit(ctx, out, Event{Kind: EventDone})
	}()

	return out
}

// release runs one segment past the guard and emits the result. It reports
// whether the caller should keep going.
//
// Events carry the model's text verbatim, markdown included. The browser
// renders that structure; the TTS adapter will call stripMarkdown on the same
// text before synthesis. One source, two renderings — flattening it here would
// serve the speech path at the screen's expense.
func (p *Pipeline) release(ctx context.Context, out chan<- Event, seg Segment) bool {
	verdict := p.guard.Check(ctx, seg.Text)
	switch {
	case verdict.OK:
		return p.emit(ctx, out, Event{
			Kind: EventSentence,
			Text: seg.Text,
			Sep:  seg.Sep,
		})
	case verdict.Replacement != "":
		p.log.Warn("guard redacted sentence", "reason", verdict.Reason)
		return p.emit(ctx, out, Event{
			Kind: EventSentence,
			Text: verdict.Replacement,
			Sep:  seg.Sep,
		})
	default:
		p.log.Warn("guard blocked sentence", "reason", verdict.Reason)
		return p.emit(ctx, out, Event{Kind: EventBlocked})
	}
}

func (p *Pipeline) emit(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
