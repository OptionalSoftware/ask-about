package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/optionalsoftware/ask-about/config"
)

// The adapters are driven against a local server rather than a vendor. Every
// one of them takes a base_url, which is what makes that possible — and what
// makes the usage extraction testable at all. It differs per vendor and it is
// what the spend caps read, so a mistake here mis-bills silently.

// drain collects a stream into its text and its usage.
func drain(t *testing.T, p Provider) (string, *Usage, error) {
	t.Helper()
	chunks, err := p.Stream(t.Context(), Request{
		System:   "system prompt",
		Messages: []Message{{Role: RoleUser, Text: "a question"}},
	})
	if err != nil {
		return "", nil, err
	}
	var text strings.Builder
	var usage *Usage
	for c := range chunks {
		if c.Err != nil {
			return text.String(), usage, c.Err
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		text.WriteString(c.Text)
	}
	return text.String(), usage, nil
}

// sse writes Server-Sent Events the way both streaming SDKs expect them.
func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, e := range events {
		fmt.Fprintf(w, "%s\n\n", e)
	}
}

// Anthropic reports all four counts, and the cache write carries a premium, so
// keeping it separate from uncached input is the whole point.
func TestAnthropicUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Errorf("api key header = %q", got)
		}
		sse(w,
			`event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":11,"cache_read_input_tokens":27123,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"An answer."}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":65}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		)
	}))
	defer srv.Close()

	p, err := New(config.LLM{Vendor: "anthropic", Model: "claude-sonnet-5",
		APIKeyRaw: "test-key", BaseURL: srv.URL, MaxTokens: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text, usage, err := drain(t, p)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if text != "An answer." {
		t.Errorf("text = %q", text)
	}
	if usage == nil {
		t.Fatal("no usage reported — the caps would see this turn as free")
	}
	if usage.InputTokens != 11 {
		t.Errorf("input = %d, want 11", usage.InputTokens)
	}
	if usage.CacheReadTokens != 27123 {
		t.Errorf("cache read = %d, want 27123", usage.CacheReadTokens)
	}
	if usage.OutputTokens != 65 {
		t.Errorf("output = %d, want 65 (message_delta carries the final count)", usage.OutputTokens)
	}
	if usage.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", usage.Model)
	}
}

// A cold cache pays a write, which costs more than uncached input. Reporting
// it as input would understate the bill.
func TestAnthropicCacheWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sse(w,
			`event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":11,"cache_read_input_tokens":0,"cache_creation_input_tokens":27123,"output_tokens":1}}}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":65}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		)
	}))
	defer srv.Close()

	p, _ := New(config.LLM{Vendor: "anthropic", Model: "claude-sonnet-5",
		APIKeyRaw: "k", BaseURL: srv.URL, MaxTokens: 100})
	_, usage, err := drain(t, p)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if usage.CacheWriteTokens != 27123 {
		t.Errorf("cache write = %d, want 27123", usage.CacheWriteTokens)
	}
	if usage.CacheReadTokens != 0 {
		t.Errorf("cache read = %d, want 0", usage.CacheReadTokens)
	}
}

// OpenAI sends usage on a final chunk with no choices. The empty-choices guard
// would skip it, so it has to be read first — this is the guard that has never
// run against a real response.
func TestOpenAIUsageArrivesOnAChoicelessChunk(t *testing.T) {
	var sawIncludeUsage bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if so, ok := body["stream_options"].(map[string]any); ok {
			sawIncludeUsage, _ = so["include_usage"].(bool)
		}
		sse(w,
			`data: {"id":"c","object":"chat.completion.chunk","model":"gpt-x","choices":[{"index":0,"delta":{"content":"An answer."}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","model":"gpt-x","choices":[],"usage":{"prompt_tokens":27134,"completion_tokens":65,"total_tokens":27199,"prompt_tokens_details":{"cached_tokens":27123}}}`,
			`data: [DONE]`,
		)
	}))
	defer srv.Close()

	p, err := New(config.LLM{Vendor: "openai", Model: "gpt-x",
		APIKeyRaw: "k", BaseURL: srv.URL, MaxTokens: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text, usage, err := drain(t, p)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !sawIncludeUsage {
		t.Error("stream_options.include_usage was not sent — usage would never arrive")
	}
	if text != "An answer." {
		t.Errorf("text = %q", text)
	}
	if usage == nil {
		t.Fatal("no usage — the choiceless chunk was skipped")
	}
	// Cached tokens are reported inside prompt_tokens, so uncached input is
	// the difference. Reporting the whole prompt as input would price cache
	// reads at ten times what they cost.
	if usage.InputTokens != 11 {
		t.Errorf("input = %d, want 11 (27134 total minus 27123 cached)", usage.InputTokens)
	}
	if usage.CacheReadTokens != 27123 {
		t.Errorf("cache read = %d, want 27123", usage.CacheReadTokens)
	}
	if usage.OutputTokens != 65 {
		t.Errorf("output = %d, want 65", usage.OutputTokens)
	}
}

// Bedrock's Converse is a single buffered response, not a stream, and reports
// only two counts. The cache columns must stay zero rather than be guessed at.
func TestBedrockUsage(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":{"message":{"content":[{"text":"An answer."}]}},
		                "usage":{"inputTokens":27134,"outputTokens":65}}`)
	}))
	defer srv.Close()

	p, err := New(config.LLM{Vendor: "bedrock", Model: "us.amazon.nova-2-lite-v1:0",
		APIKeyRaw: "bedrock-key", BaseURL: srv.URL, MaxTokens: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text, usage, err := drain(t, p)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if text != "An answer." {
		t.Errorf("text = %q", text)
	}
	// Colons survive: they are legal in a path segment, and escaping them
	// would send Bedrock a model id it does not recognise.
	if !strings.Contains(gotPath, "us.amazon.nova-2-lite-v1:0") {
		t.Errorf("path = %q, want the model id intact", gotPath)
	}
	if gotAuth != "Bearer bedrock-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if usage.InputTokens != 27134 || usage.OutputTokens != 65 {
		t.Errorf("usage = %+v", usage)
	}
	if usage.CacheReadTokens != 0 || usage.CacheWriteTokens != 0 {
		t.Errorf("bedrock reports no cache counts, got %+v", usage)
	}
}

// An error status must not be read as an empty answer.
func TestBedrockErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"Too many requests"}`)
	}))
	defer srv.Close()

	p, _ := New(config.LLM{Vendor: "bedrock", Model: "m",
		APIKeyRaw: "k", BaseURL: srv.URL, MaxTokens: 100})
	_, _, err := drain(t, p)
	if err == nil {
		t.Fatal("a 429 was treated as a successful empty answer")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %v, want it to carry the status", err)
	}
}

// A vendor failure has to reach the caller rather than looking like a short
// answer, or a failed turn is indistinguishable from a terse one.
func TestStreamErrorsSurface(t *testing.T) {
	for _, vendor := range []string{"anthropic", "openai"} {
		t.Run(vendor, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"upstream fell over"}}`)
			}))
			defer srv.Close()

			p, _ := New(config.LLM{Vendor: vendor, Model: "m",
				APIKeyRaw: "k", BaseURL: srv.URL, MaxTokens: 100})
			_, _, err := drain(t, p)
			if err == nil {
				t.Fatal("a 500 produced no error")
			}
		})
	}
}

// The model id comes from config and lands in a URL path, so a separator in it
// must not be able to reach a different endpoint.
func TestBedrockModelIDCannotEscapeThePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":{"message":{"content":[{"text":"x"}]}},"usage":{"inputTokens":1,"outputTokens":1}}`)
	}))
	defer srv.Close()

	p, _ := New(config.LLM{Vendor: "bedrock", Model: "../../admin",
		APIKeyRaw: "k", BaseURL: srv.URL, MaxTokens: 10})
	if _, _, err := drain(t, p); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Contains(gotPath, "../") {
		t.Errorf("path = %q — the model id escaped its segment", gotPath)
	}
	if !strings.HasPrefix(gotPath, "/model/") {
		t.Errorf("path = %q, want it still under /model/", gotPath)
	}
}
