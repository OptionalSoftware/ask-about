package pipeline

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/optionalsoftware/ask-about/llm"
)

// fakeProvider replays a fixed set of chunks, so pipeline behaviour can be
// tested without a network call.
type fakeProvider struct{ chunks []string }

func (fakeProvider) Name() string { return "fake" }

func (f fakeProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	out := make(chan llm.Chunk)
	go func() {
		defer close(out)
		for _, c := range f.chunks {
			select {
			case out <- llm.Chunk{Text: c}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func run(t *testing.T, chunks ...string) []Event {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(fakeProvider{chunks: chunks}, PassThrough{}, log)

	var got []Event
	for ev := range p.Run(context.Background(), llm.Request{}) {
		got = append(got, ev)
	}
	return got
}

// The pipeline passes the model's text through verbatim — markdown included,
// since the browser renders it — and reports the separator it actually saw.
// Flattening belongs at the TTS boundary, not here.
func TestPipelinePreservesMarkupAndReportsSeparators(t *testing.T) {
	events := run(t,
		"Three areas:\n",
		"- **Billing** — invoices and payments.\n",
		"- **Search** — indexing and ranking.",
	)

	var sentences []Event
	for _, ev := range events {
		if ev.Kind == EventSentence {
			sentences = append(sentences, ev)
		}
	}

	if len(sentences) != 3 {
		t.Fatalf("got %d sentences, want 3: %+v", len(sentences), sentences)
	}

	if got := sentences[1].Text; !contains(got, "**Billing**") {
		t.Errorf("markdown should reach the client for rendering, got %q", got)
	}

	if sentences[1].Sep != "\n" {
		t.Errorf("separator lost: got %q, want %q", sentences[1].Sep, "\n")
	}
	if sentences[2].Sep != "\n" {
		t.Errorf("separator lost: got %q, want %q", sentences[2].Sep, "\n")
	}

	if last := events[len(events)-1]; last.Kind != EventDone {
		t.Errorf("stream did not end with done: %+v", last)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
