package oairouter

import (
	"context"
	"net/url"

	"github.com/stevemurr/oairouter/types"
)

// BackendType identifies the LLM backend software.
type BackendType string

const (
	BackendVLLM     BackendType = "vllm"
	BackendOllama   BackendType = "ollama"
	BackendLlamaCpp BackendType = "llamacpp"
	BackendLMStudio BackendType = "lmstudio"
	BackendGeneric  BackendType = "generic"
)

// Backend represents an LLM inference server.
type Backend interface {
	// Identity
	ID() string
	Type() BackendType
	BaseURL() *url.URL

	// Model information
	Models(ctx context.Context) ([]types.Model, error)

	// Health
	HealthCheck(ctx context.Context) error
	IsHealthy() bool

	// OpenAI-compatible request handlers
	ChatCompletion(ctx context.Context, req *types.ChatCompletionRequest) (*types.ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, req *types.ChatCompletionRequest) (<-chan StreamEvent, error)
	Completion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error)
	CompletionStream(ctx context.Context, req *types.CompletionRequest) (<-chan StreamEvent, error)
	Embeddings(ctx context.Context, req *types.EmbeddingsRequest) (*types.EmbeddingsResponse, error)
}

// StreamEvent represents an event in a streaming response.
type StreamEvent struct {
	// Data is the raw SSE data (JSON string for chunks, "[DONE]" for termination)
	Data string

	// Err is set if an error occurred during streaming
	Err error

	// Done indicates this is the final event
	Done bool
}
