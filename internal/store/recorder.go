package store

import (
	"context"
	"log/slog"

	"github.com/optionalsoftware/ask-about/internal/llm"
	"github.com/optionalsoftware/ask-about/internal/pipeline"
)

// Recorder adapts the pipeline's Observer to a Store.
//
// It lives here rather than in pipeline so the pipeline keeps knowing nothing
// about persistence — it emits a Result and is done.
type Recorder struct {
	store  Store
	vendor string
	log    *slog.Logger
}

func NewRecorder(s Store, vendor string, log *slog.Logger) *Recorder {
	return &Recorder{store: s, vendor: vendor, log: log}
}

// RunFinished persists one completed turn.
//
// Failures are logged and swallowed. A database problem must never surface to
// someone asking a question — by the time this runs they already have their
// answer, and losing a log row is a smaller failure than a broken reply.
func (r *Recorder) RunFinished(ctx context.Context, req llm.Request, res pipeline.Result) {
	if r == nil || r.store == nil {
		return
	}

	turn := Turn{
		Vendor:    r.vendor,
		SessionID: req.Session,
		InviteID:  req.Invite,
		Question:  lastUserMessage(req),
		Answer:    res.Answer,
		Usage: Usage{
			InputTokens:      res.Usage.InputTokens,
			CacheReadTokens:  res.Usage.CacheReadTokens,
			CacheWriteTokens: res.Usage.CacheWriteTokens,
			OutputTokens:     res.Usage.OutputTokens,
			Model:            res.Usage.Model,
		},
		Latency: res.Latency,
		Blocked: res.Blocked,
	}
	if res.Err != nil {
		turn.Err = res.Err.Error()
	}

	if err := r.store.RecordTurn(ctx, &turn); err != nil {
		r.log.Error("could not record turn", "err", err)
	}
}

// lastUserMessage pulls the question out of the request. The history is
// replayed in full on every turn, so only the final user message is new — the
// rest is already stored as its own row.
func lastUserMessage(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Text
		}
	}
	return ""
}
