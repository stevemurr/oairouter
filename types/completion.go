package types

// CompletionRequest represents an OpenAI legacy completion request.
type CompletionRequest struct {
	Model            string   `json:"model"`
	Prompt           any      `json:"prompt"` // string or []string
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	N                *int     `json:"n,omitempty"`
	Stream           bool     `json:"stream,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int `json:"logit_bias,omitempty"`
	User             string   `json:"user,omitempty"`
	Seed             *int     `json:"seed,omitempty"`
	Echo             bool     `json:"echo,omitempty"`
	BestOf           *int     `json:"best_of,omitempty"`
	Logprobs         *int     `json:"logprobs,omitempty"`
}

// CompletionResponse represents an OpenAI legacy completion response.
type CompletionResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"` // text_completion
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	SystemFingerprint string             `json:"system_fingerprint,omitempty"`
	Choices           []CompletionChoice `json:"choices"`
	Usage             *Usage             `json:"usage,omitempty"`
}

// CompletionChoice represents a legacy completion choice.
type CompletionChoice struct {
	Text         string   `json:"text"`
	Index        int      `json:"index"`
	FinishReason string   `json:"finish_reason"`
	Logprobs     *Logprobs `json:"logprobs,omitempty"`
}

// Logprobs represents log probability information.
type Logprobs struct {
	Tokens        []string             `json:"tokens,omitempty"`
	TokenLogprobs []float64            `json:"token_logprobs,omitempty"`
	TopLogprobs   []map[string]float64 `json:"top_logprobs,omitempty"`
	TextOffset    []int                `json:"text_offset,omitempty"`
}

// CompletionChunk represents a streaming completion chunk.
type CompletionChunk struct {
	ID                string                  `json:"id"`
	Object            string                  `json:"object"` // text_completion
	Created           int64                   `json:"created"`
	Model             string                  `json:"model"`
	SystemFingerprint string                  `json:"system_fingerprint,omitempty"`
	Choices           []CompletionChunkChoice `json:"choices"`
}

// CompletionChunkChoice represents a streaming completion choice.
type CompletionChunkChoice struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason"`
}
