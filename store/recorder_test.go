package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/optionalsoftware/ask-about/llm"
	"github.com/optionalsoftware/ask-about/pipeline"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRecorderWritesTheTurn(t *testing.T) {
	db := testStore(t)
	inv, err := db.CreateInvite(t.Context(), "Acme Corp", "", "tok1", "hash-1", nil)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	r := NewRecorder(db, "anthropic", quietLog())
	r.RunFinished(t.Context(), llm.Request{
		Session: "visit-1",
		Invite:  inv.ID,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "first question"},
			{Role: llm.RoleAssistant, Text: "first answer"},
			{Role: llm.RoleUser, Text: "the new question"},
		},
	}, pipeline.Result{
		Answer:  "the answer",
		Latency: 1500 * time.Millisecond,
		Usage: llm.Usage{
			Model: "claude-sonnet-5", InputTokens: 11,
			CacheReadTokens: 27123, OutputTokens: 65,
		},
	})

	sessions, err := db.SessionsForInvite(t.Context(), inv.ID)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	got := sessions[0].Turns[0]

	// The history is replayed in full every turn, so only the last user
	// message is new. Storing an earlier one would file the answer under the
	// wrong question.
	if got.Question != "the new question" {
		t.Errorf("question = %q, want the most recent one", got.Question)
	}
	if got.Answer != "the answer" {
		t.Errorf("answer = %q", got.Answer)
	}
	if got.Usage.CacheReadTokens != 27123 || got.Usage.OutputTokens != 65 {
		t.Errorf("usage did not survive: %+v", got.Usage)
	}
	if got.Usage.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", got.Usage.Model)
	}
	if got.Latency != 1500*time.Millisecond {
		t.Errorf("latency = %v", got.Latency)
	}
}

// A failed turn still cost tokens and still shows what someone asked.
func TestRecorderKeepsFailedTurns(t *testing.T) {
	db := testStore(t)
	inv, _ := db.CreateInvite(t.Context(), "Acme Corp", "", "tok1", "hash-1", nil)

	NewRecorder(db, "anthropic", quietLog()).RunFinished(t.Context(),
		llm.Request{Session: "v1", Invite: inv.ID,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "q"}}},
		pipeline.Result{Err: errors.New("503 Service Unavailable"),
			Usage: llm.Usage{Model: "m", CacheWriteTokens: 27123}})

	sessions, _ := db.SessionsForInvite(t.Context(), inv.ID)
	if len(sessions) != 1 {
		t.Fatalf("a failed turn was not recorded")
	}
	turn := sessions[0].Turns[0]
	if turn.Err == "" {
		t.Error("the failure reason was not stored")
	}
	// The visitor saw a readable sentence; the row keeps the real error.
	if turn.Err != "503 Service Unavailable" {
		t.Errorf("stored error = %q, want the underlying one", turn.Err)
	}
	if turn.Usage.CacheWriteTokens != 27123 {
		t.Error("a failed turn lost its token cost")
	}
}

// failingStore stands in for a database that has gone wrong.
type failingStore struct{ Store }

func (failingStore) RecordTurn(context.Context, *Turn) error {
	return errors.New("disk on fire")
}

// A storage failure must not reach the visitor. By the time this runs they
// already have their answer, and losing a log row beats breaking a reply.
func TestRecorderSwallowsStoreErrors(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("a store failure panicked: %v", p)
		}
	}()
	NewRecorder(failingStore{}, "anthropic", quietLog()).RunFinished(
		t.Context(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "q"}}},
		pipeline.Result{Answer: "a"})
}

// A nil recorder is what a storage-less deployment has.
func TestNilRecorderIsSafe(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("nil recorder panicked: %v", p)
		}
	}()
	var r *Recorder
	r.RunFinished(t.Context(), llm.Request{}, pipeline.Result{})
	NewRecorder(nil, "anthropic", quietLog()).RunFinished(
		t.Context(), llm.Request{}, pipeline.Result{})
}

// A request with no user message must not store an empty question against a
// real answer.
func TestLastUserMessage(t *testing.T) {
	tests := []struct {
		name string
		msgs []llm.Message
		want string
	}{
		{"single", []llm.Message{{Role: llm.RoleUser, Text: "only"}}, "only"},
		{"picks the last", []llm.Message{
			{Role: llm.RoleUser, Text: "first"},
			{Role: llm.RoleAssistant, Text: "answer"},
			{Role: llm.RoleUser, Text: "second"},
		}, "second"},
		{"ignores a trailing assistant message", []llm.Message{
			{Role: llm.RoleUser, Text: "asked"},
			{Role: llm.RoleAssistant, Text: "replied"},
		}, "asked"},
		{"none", []llm.Message{{Role: llm.RoleAssistant, Text: "replied"}}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastUserMessage(llm.Request{Messages: tc.msgs}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
