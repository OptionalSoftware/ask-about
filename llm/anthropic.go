package llm

import (
	"context"
	"log/slog"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/optionalsoftware/ask-about/config"
)

type anthropicProvider struct {
	client    anthropic.Client
	model     string
	maxTokens int64
	effort    anthropic.OutputConfigEffort
}

func newAnthropic(cfg config.LLM, key string) Provider {
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicProvider{
		client:    anthropic.NewClient(opts...),
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		effort:    anthropic.OutputConfigEffort(cfg.Effort),
	}
}

func (a *anthropicProvider) Name() string { return "anthropic" }

func (a *anthropicProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: a.maxTokens,
		Messages:  toAnthropicMessages(req.Messages),
	}

	// The corpus is the bulk of every request and never changes between turns,
	// so it is worth a cache breakpoint. Render order is tools -> system ->
	// messages, which puts the whole system block behind one marker.
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}
	if a.effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: a.effort}
	}

	out := make(chan Chunk)
	go func() {
		defer close(out)

		// Accumulate rebuilds the full message from the event stream, which is
		// how usage arrives: message_start carries the input and cache counts,
		// message_delta the cumulative output. Letting the SDK assemble it
		// avoids hand-reading two event types and getting the totals wrong.
		var message anthropic.Message

		stream := a.client.Messages.NewStreaming(ctx, params)
		for stream.Next() {
			event := stream.Current()
			if err := message.Accumulate(event); err != nil {
				// Accounting failed, but the text is still good. Keep going and
				// report no usage rather than dropping the answer.
				a.logAccumulateError(err)
			}

			delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent)
			if !ok {
				continue
			}
			text, ok := delta.Delta.AsAny().(anthropic.TextDelta)
			if !ok {
				// Thinking deltas land here too. Nothing to show the user.
				continue
			}
			select {
			case out <- Chunk{Text: text.Text}:
			case <-ctx.Done():
				return
			}
		}
		if err := stream.Err(); err != nil {
			select {
			case out <- Chunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}

		usage := Usage{
			InputTokens:      message.Usage.InputTokens,
			CacheReadTokens:  message.Usage.CacheReadInputTokens,
			CacheWriteTokens: message.Usage.CacheCreationInputTokens,
			OutputTokens:     message.Usage.OutputTokens,
			Model:            string(message.Model),
		}
		if usage.Model == "" {
			usage.Model = a.model
		}
		select {
		case out <- Chunk{Usage: &usage}:
		case <-ctx.Done():
		}
	}()

	return out, nil
}

// logAccumulateError exists so a malformed event degrades to "no usage"
// rather than a lost answer. Usage is bookkeeping; the text is the product.
func (a *anthropicProvider) logAccumulateError(err error) {
	slog.Warn("anthropic: could not accumulate stream event", "err", err)
}

func toAnthropicMessages(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		block := anthropic.NewTextBlock(m.Text)
		if m.Role == RoleAssistant {
			out = append(out, anthropic.NewAssistantMessage(block))
			continue
		}
		out = append(out, anthropic.NewUserMessage(block))
	}
	return out
}
