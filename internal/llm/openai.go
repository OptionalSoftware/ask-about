package llm

import (
	"context"

	"github.com/optionalsoftware/ask-about/internal/config"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// openaiProvider covers every OpenAI-compatible API, not just OpenAI: xAI
// (Grok), Groq, DeepSeek, Together, OpenRouter, Ollama, and any local vLLM
// server all speak this protocol. The only difference is base_url and model.
type openaiProvider struct {
	client    openai.Client
	model     string
	maxTokens int64
}

func newOpenAI(cfg config.LLM, key string) Provider {
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &openaiProvider{
		client:    openai.NewClient(opts...),
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
	}
}

func (o *openaiProvider) Name() string { return "openai" }

func (o *openaiProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		if m.Role == RoleAssistant {
			msgs = append(msgs, openai.AssistantMessage(m.Text))
			continue
		}
		msgs = append(msgs, openai.UserMessage(m.Text))
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(o.model),
		Messages: msgs,
		// Usage is omitted from streamed responses unless asked for, and it
		// arrives on a final chunk that carries no choices.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if o.maxTokens > 0 {
		// max_tokens rather than max_completion_tokens: OpenAI deprecated the
		// former, but it is what the compatible servers above actually
		// implement. Revisit if we point this adapter at a reasoning model.
		params.MaxTokens = openai.Int(o.maxTokens)
	}

	out := make(chan Chunk)
	go func() {
		defer close(out)

		var usage Usage
		usage.Model = o.model

		stream := o.client.Chat.Completions.NewStreaming(ctx, params)
		for stream.Next() {
			chunk := stream.Current()

			// The usage chunk arrives last and has no choices, so it must be
			// read before the empty-choices guard below skips it.
			if chunk.Usage.TotalTokens > 0 {
				usage.InputTokens = chunk.Usage.PromptTokens -
					chunk.Usage.PromptTokensDetails.CachedTokens
				usage.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
				usage.OutputTokens = chunk.Usage.CompletionTokens
			}
			if chunk.Model != "" {
				usage.Model = chunk.Model
			}

			if len(chunk.Choices) == 0 {
				continue
			}
			text := chunk.Choices[0].Delta.Content
			if text == "" {
				continue
			}
			select {
			case out <- Chunk{Text: text}:
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

		// OpenAI reports no cache-write tokens: writes are implicit and
		// unbilled, unlike Anthropic where they carry a premium.
		select {
		case out <- Chunk{Usage: &usage}:
		case <-ctx.Done():
		}
	}()

	return out, nil
}
