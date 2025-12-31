package backends

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stevemurr/oairouter"
	"github.com/stevemurr/oairouter/types"
)

// GenericBackend proxies requests to any OpenAI-compatible server.
type GenericBackend struct {
	id          string
	backendType oairouter.BackendType
	baseURL     *url.URL
	httpClient  *http.Client

	healthy atomic.Bool
	mu      sync.RWMutex
	models  []types.Model
}

// GenericBackendOption configures a GenericBackend.
type GenericBackendOption func(*GenericBackend)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) GenericBackendOption {
	return func(b *GenericBackend) {
		b.httpClient = client
	}
}

// WithBackendType sets the backend type.
func WithBackendType(t oairouter.BackendType) GenericBackendOption {
	return func(b *GenericBackend) {
		b.backendType = t
	}
}

// NewGenericBackend creates a new generic OpenAI-compatible backend.
func NewGenericBackend(id string, baseURL string, opts ...GenericBackendOption) (*GenericBackend, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	b := &GenericBackend{
		id:          id,
		backendType: oairouter.BackendGeneric,
		baseURL:     u,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for completions
		},
	}
	b.healthy.Store(true)

	for _, opt := range opts {
		opt(b)
	}

	return b, nil
}

func (b *GenericBackend) ID() string {
	return b.id
}

func (b *GenericBackend) Type() oairouter.BackendType {
	return b.backendType
}

func (b *GenericBackend) BaseURL() *url.URL {
	return b.baseURL
}

func (b *GenericBackend) IsHealthy() bool {
	return b.healthy.Load()
}

func (b *GenericBackend) setHealthy(healthy bool) {
	b.healthy.Store(healthy)
}

func (b *GenericBackend) HealthCheck(ctx context.Context) error {
	// Try to fetch models as a health check
	_, err := b.Models(ctx)
	b.setHealthy(err == nil)
	return err
}

func (b *GenericBackend) Models(ctx context.Context) ([]types.Model, error) {
	u := b.baseURL.JoinPath("/v1/models")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models request failed: %s - %s", resp.Status, string(body))
	}

	var modelsResp types.ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to decode models response: %w", err)
	}

	b.mu.Lock()
	b.models = modelsResp.Data
	b.mu.Unlock()

	return modelsResp.Data, nil
}

func (b *GenericBackend) ChatCompletion(ctx context.Context, chatReq *types.ChatCompletionRequest) (*types.ChatCompletionResponse, error) {
	u := b.baseURL.JoinPath("/v1/chat/completions")

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chat completion failed: %s - %s", resp.Status, string(respBody))
	}

	var chatResp types.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode chat response: %w", err)
	}

	return &chatResp, nil
}

// streamRequest handles the common SSE streaming pattern for any endpoint.
func (b *GenericBackend) streamRequest(ctx context.Context, endpoint string, body []byte) (<-chan oairouter.StreamEvent, error) {
	u := b.baseURL.JoinPath(endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("stream request failed: %s - %s", resp.Status, string(respBody))
	}

	events := make(chan oairouter.StreamEvent, 100)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				events <- oairouter.StreamEvent{Err: ctx.Err(), Done: true}
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				// Send error event for non-EOF errors, but always send Done
				// to ensure the stream terminates properly for the client
				if err != io.EOF {
					events <- oairouter.StreamEvent{Err: err, Done: true}
				} else {
					// EOF without [DONE] - signal clean termination
					events <- oairouter.StreamEvent{Done: true}
				}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				events <- oairouter.StreamEvent{Data: data, Done: true}
				return
			}

			events <- oairouter.StreamEvent{Data: data}
		}
	}()

	return events, nil
}

func (b *GenericBackend) ChatCompletionStream(ctx context.Context, chatReq *types.ChatCompletionRequest) (<-chan oairouter.StreamEvent, error) {
	chatReq.Stream = true
	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, err
	}
	return b.streamRequest(ctx, "/v1/chat/completions", body)
}

func (b *GenericBackend) Completion(ctx context.Context, compReq *types.CompletionRequest) (*types.CompletionResponse, error) {
	u := b.baseURL.JoinPath("/v1/completions")

	body, err := json.Marshal(compReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("completion failed: %s - %s", resp.Status, string(respBody))
	}

	var compResp types.CompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&compResp); err != nil {
		return nil, fmt.Errorf("failed to decode completion response: %w", err)
	}

	return &compResp, nil
}

func (b *GenericBackend) CompletionStream(ctx context.Context, compReq *types.CompletionRequest) (<-chan oairouter.StreamEvent, error) {
	compReq.Stream = true
	body, err := json.Marshal(compReq)
	if err != nil {
		return nil, err
	}
	return b.streamRequest(ctx, "/v1/completions", body)
}

func (b *GenericBackend) Embeddings(ctx context.Context, embReq *types.EmbeddingsRequest) (*types.EmbeddingsResponse, error) {
	u := b.baseURL.JoinPath("/v1/embeddings")

	body, err := json.Marshal(embReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings failed: %s - %s", resp.Status, string(respBody))
	}

	var embResp types.EmbeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("failed to decode embeddings response: %w", err)
	}

	return &embResp, nil
}
