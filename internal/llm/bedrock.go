package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/optionalsoftware/ask-about/internal/config"
)

// bedrockProvider talks to Bedrock's Converse API over plain HTTP with a
// bearer API key — no AWS SDK, no SigV4, no dependencies.
//
// It calls /converse (buffered) rather than /converse-stream. The streaming
// endpoint returns application/vnd.amazon.eventstream: length-prefixed binary
// frames with CRC32 checksums, which needs a real decoder rather than a line
// scanner. Until that's worth writing, the whole response arrives as a single
// chunk. Everything downstream — sentence splitting, the guard stage, the UI —
// is unaffected; only token-by-token flow is missing.
type bedrockProvider struct {
	http      *http.Client
	baseURL   string
	model     string
	apiKey    string
	maxTokens int64
}

func newBedrock(cfg config.LLM, key string) Provider {
	return &bedrockProvider{
		http:      &http.Client{Timeout: 120 * time.Second},
		baseURL:   cfg.BaseURL,
		model:     cfg.Model,
		apiKey:    key,
		maxTokens: cfg.MaxTokens,
	}
}

func (b *bedrockProvider) Name() string { return "bedrock" }

type bedrockText struct {
	Text string `json:"text"`
}

type bedrockMessage struct {
	Role    string        `json:"role"`
	Content []bedrockText `json:"content"`
}

type bedrockRequest struct {
	Messages        []bedrockMessage `json:"messages"`
	System          []bedrockText    `json:"system,omitempty"`
	InferenceConfig struct {
		MaxTokens int64 `json:"maxTokens,omitempty"`
	} `json:"inferenceConfig"`
}

type bedrockResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	// Converse reports only two counts. There is no cache breakdown here,
	// so cache read and write stay zero rather than being guessed at.
	Usage struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
	StopReason string `json:"stopReason"`
	Message    string `json:"message"` // populated on error responses
}

func (b *bedrockProvider) Stream(ctx context.Context, req Request) (<-chan Chunk, error) {
	body := bedrockRequest{Messages: make([]bedrockMessage, 0, len(req.Messages))}
	if req.System != "" {
		body.System = []bedrockText{{Text: req.System}}
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == RoleAssistant {
			role = "assistant"
		}
		body.Messages = append(body.Messages, bedrockMessage{
			Role:    role,
			Content: []bedrockText{{Text: m.Text}},
		})
	}
	body.InferenceConfig.MaxTokens = b.maxTokens

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: encode request: %w", err)
	}

	// The model id comes from config and lands in a URL path. PathEscape
	// leaves the colons in "us.amazon.nova-2-lite-v1:0" alone — they are legal
	// in a path segment — and escapes the separators that would otherwise let
	// a model id reach a different endpoint.
	endpoint := fmt.Sprintf("%s/model/%s/converse", b.baseURL, url.PathEscape(b.model))

	out := make(chan Chunk)
	go func() {
		defer close(out)

		text, usage, err := b.converse(ctx, endpoint, payload)
		if err != nil {
			select {
			case out <- Chunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case out <- Chunk{Text: text}:
		case <-ctx.Done():
			return
		}
		select {
		case out <- Chunk{Usage: &usage}:
		case <-ctx.Done():
		}
	}()

	return out, nil
}

func (b *bedrockProvider) converse(ctx context.Context, endpoint string, payload []byte) (string, Usage, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, fmt.Errorf("bedrock: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.http.Do(httpReq)
	if err != nil {
		return "", Usage{}, fmt.Errorf("bedrock: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", Usage{}, fmt.Errorf("bedrock: read response: %w", err)
	}

	var parsed bedrockResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", Usage{}, fmt.Errorf("bedrock: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Message != "" {
			return "", Usage{}, fmt.Errorf("bedrock: status %d: %s", resp.StatusCode, parsed.Message)
		}
		return "", Usage{}, fmt.Errorf("bedrock: status %d", resp.StatusCode)
	}

	var text string
	for _, block := range parsed.Output.Message.Content {
		text += block.Text
	}
	return text, Usage{
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		Model:        b.model,
	}, nil
}
