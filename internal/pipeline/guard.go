package pipeline

import "context"

// Verdict is a guard's decision about one sentence.
type Verdict struct {
	// OK is true when the sentence may be released to the client.
	OK bool
	// Replacement, when non-empty and OK is false, is sent instead of the
	// original — for redaction rather than outright blocking.
	Replacement string
	// Reason is for logging. It is never shown to the user.
	Reason string
}

// Guard inspects one sentence before it reaches the client.
//
// No guard is implemented yet, but the stage is real and always runs:
// sentence splitting is needed for TTS regardless, so the pipeline has this
// shape either way. Adding one later means writing an implementation of this
// interface and passing it to New — no re-plumbing.
type Guard interface {
	Check(ctx context.Context, sentence string) Verdict
}

// PassThrough approves everything. This is the default.
type PassThrough struct{}

func (PassThrough) Check(context.Context, string) Verdict {
	return Verdict{OK: true}
}
