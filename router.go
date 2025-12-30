package oairouter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stevemurr/oairouter/streaming"
	"github.com/stevemurr/oairouter/types"
)

// Discoverer finds and monitors LLM backends.
type Discoverer interface {
	Name() string
	Discover(ctx context.Context) ([]Backend, error)
	Watch(ctx context.Context) (<-chan DiscoveryEvent, error)
}

// DiscoveryEvent signals backend changes.
type DiscoveryEvent struct {
	Type    EventType
	Backend Backend
}

// EventType represents a discovery event type.
type EventType string

const (
	EventAdded   EventType = "added"
	EventRemoved EventType = "removed"
	EventUpdated EventType = "updated"
)

// Router is the main OpenAI-compatible proxy.
type Router struct {
	registry            *BackendRegistry
	discoverers         []Discoverer
	httpClient          *http.Client
	logger              *slog.Logger
	defaultBackend      string
	healthCheckInterval time.Duration

	mux     *http.ServeMux
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started atomic.Bool
}

// NewRouter creates a new router with functional options.
func NewRouter(opts ...Option) (*Router, error) {
	r := &Router{
		registry:            NewBackendRegistry(),
		httpClient:          &http.Client{Timeout: 5 * time.Minute},
		logger:              slog.Default(),
		healthCheckInterval: 30 * time.Second,
		mux:                 http.NewServeMux(),
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}

	// Register routes
	r.mux.HandleFunc("POST /v1/chat/completions", r.handleChatCompletions)
	r.mux.HandleFunc("POST /v1/completions", r.handleCompletions)
	r.mux.HandleFunc("POST /v1/embeddings", r.handleEmbeddings)
	r.mux.HandleFunc("GET /v1/models", r.handleListModels)
	r.mux.HandleFunc("GET /v1/models/{model...}", r.handleGetModel)
	r.mux.HandleFunc("GET /health", r.handleHealth)

	return r, nil
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// Start begins discovery and health monitoring.
func (r *Router) Start(ctx context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return nil
	}

	ctx, r.cancel = context.WithCancel(ctx)

	// Run initial discovery
	for _, d := range r.discoverers {
		backends, err := d.Discover(ctx)
		if err != nil {
			r.logger.Warn("discovery failed", "discoverer", d.Name(), "error", err)
			continue
		}

		for _, b := range backends {
			if err := r.registry.Register(ctx, b); err != nil {
				r.logger.Warn("failed to register backend", "backend", b.ID(), "error", err)
			} else {
				r.logger.Info("registered backend", "id", b.ID(), "type", b.Type(), "url", b.BaseURL())
			}
		}

		// Start watching for changes
		events, err := d.Watch(ctx)
		if err != nil {
			r.logger.Warn("failed to start watch", "discoverer", d.Name(), "error", err)
			continue
		}

		r.wg.Add(1)
		go r.watchEvents(ctx, d.Name(), events)
	}

	// Start health check loop
	r.wg.Add(1)
	go r.healthCheckLoop(ctx)

	return nil
}

// Stop gracefully shuts down the router.
func (r *Router) Stop(ctx context.Context) error {
	if !r.started.CompareAndSwap(true, false) {
		return nil
	}

	if r.cancel != nil {
		r.cancel()
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Backends returns the backend registry.
func (r *Router) Backends() *BackendRegistry {
	return r.registry
}

// AddBackend manually registers a backend.
func (r *Router) AddBackend(ctx context.Context, b Backend) error {
	return r.registry.Register(ctx, b)
}

// RemoveBackend manually unregisters a backend.
func (r *Router) RemoveBackend(id string) {
	r.registry.Unregister(id)
}

func (r *Router) watchEvents(ctx context.Context, name string, events <-chan DiscoveryEvent) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			switch event.Type {
			case EventAdded:
				if err := r.registry.Register(ctx, event.Backend); err != nil {
					r.logger.Warn("failed to register backend", "backend", event.Backend.ID(), "error", err)
				} else {
					r.logger.Info("backend added", "id", event.Backend.ID(), "discoverer", name)
				}
			case EventRemoved:
				r.registry.Unregister(event.Backend.ID())
				r.logger.Info("backend removed", "id", event.Backend.ID(), "discoverer", name)
			case EventUpdated:
				if err := r.registry.RefreshModels(ctx, event.Backend.ID()); err != nil {
					r.logger.Warn("failed to refresh models", "backend", event.Backend.ID(), "error", err)
				}
			}
		}
	}
}

func (r *Router) healthCheckLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, b := range r.registry.AllBackends() {
				if err := b.HealthCheck(ctx); err != nil {
					r.logger.Debug("health check failed", "backend", b.ID(), "error", err)
				}
			}
		}
	}
}

func (r *Router) handleChatCompletions(w http.ResponseWriter, req *http.Request) {
	var chatReq types.ChatCompletionRequest
	if err := json.NewDecoder(req.Body).Decode(&chatReq); err != nil {
		types.WriteError(w, http.StatusBadRequest, types.InvalidRequestError("invalid request body: "+err.Error()))
		return
	}

	backend, ok := r.registry.LookupByModel(chatReq.Model)
	if !ok {
		// Try default backend
		if r.defaultBackend != "" {
			backend, ok = r.registry.LookupByID(r.defaultBackend)
		}
		if !ok {
			types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+chatReq.Model))
			return
		}
	}

	if chatReq.Stream {
		r.handleChatCompletionsStream(w, req, backend, &chatReq)
		return
	}

	resp, err := backend.ChatCompletion(req.Context(), &chatReq)
	if err != nil {
		r.logger.Error("chat completion failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (r *Router) handleChatCompletionsStream(w http.ResponseWriter, req *http.Request, backend Backend, chatReq *types.ChatCompletionRequest) {
	sse := streaming.NewWriter(w)
	if sse == nil {
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("streaming not supported"))
		return
	}

	events, err := backend.ChatCompletionStream(req.Context(), chatReq)
	if err != nil {
		r.logger.Error("chat completion stream failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	sse.WriteHeaders()

	for event := range events {
		if event.Err != nil {
			r.logger.Error("stream error", "backend", backend.ID(), "error", event.Err)
			break
		}

		if event.Done && event.Data == "[DONE]" {
			sse.WriteDone()
			break
		}

		if event.Data != "" {
			if err := sse.WriteData(event.Data); err != nil {
				r.logger.Debug("failed to write SSE data", "error", err)
				break
			}
		}
	}
}

func (r *Router) handleCompletions(w http.ResponseWriter, req *http.Request) {
	var compReq types.CompletionRequest
	if err := json.NewDecoder(req.Body).Decode(&compReq); err != nil {
		types.WriteError(w, http.StatusBadRequest, types.InvalidRequestError("invalid request body: "+err.Error()))
		return
	}

	backend, ok := r.registry.LookupByModel(compReq.Model)
	if !ok {
		if r.defaultBackend != "" {
			backend, ok = r.registry.LookupByID(r.defaultBackend)
		}
		if !ok {
			types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+compReq.Model))
			return
		}
	}

	if compReq.Stream {
		r.handleCompletionsStream(w, req, backend, &compReq)
		return
	}

	resp, err := backend.Completion(req.Context(), &compReq)
	if err != nil {
		r.logger.Error("completion failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (r *Router) handleCompletionsStream(w http.ResponseWriter, req *http.Request, backend Backend, compReq *types.CompletionRequest) {
	sse := streaming.NewWriter(w)
	if sse == nil {
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("streaming not supported"))
		return
	}

	events, err := backend.CompletionStream(req.Context(), compReq)
	if err != nil {
		r.logger.Error("completion stream failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	sse.WriteHeaders()

	for event := range events {
		if event.Err != nil {
			r.logger.Error("stream error", "backend", backend.ID(), "error", event.Err)
			break
		}

		if event.Done && event.Data == "[DONE]" {
			sse.WriteDone()
			break
		}

		if event.Data != "" {
			if err := sse.WriteData(event.Data); err != nil {
				r.logger.Debug("failed to write SSE data", "error", err)
				break
			}
		}
	}
}

func (r *Router) handleEmbeddings(w http.ResponseWriter, req *http.Request) {
	var embReq types.EmbeddingsRequest
	if err := json.NewDecoder(req.Body).Decode(&embReq); err != nil {
		types.WriteError(w, http.StatusBadRequest, types.InvalidRequestError("invalid request body: "+err.Error()))
		return
	}

	backend, ok := r.registry.LookupByModel(embReq.Model)
	if !ok {
		if r.defaultBackend != "" {
			backend, ok = r.registry.LookupByID(r.defaultBackend)
		}
		if !ok {
			types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+embReq.Model))
			return
		}
	}

	resp, err := backend.Embeddings(req.Context(), &embReq)
	if err != nil {
		r.logger.Error("embeddings failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (r *Router) handleListModels(w http.ResponseWriter, req *http.Request) {
	models := r.registry.AllModels(req.Context())

	resp := types.ModelsResponse{
		Object: "list",
		Data:   models,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (r *Router) handleGetModel(w http.ResponseWriter, req *http.Request) {
	modelID := req.PathValue("model")
	if modelID == "" {
		// Try to extract from path manually for older Go versions
		path := strings.TrimPrefix(req.URL.Path, "/v1/models/")
		modelID = path
	}

	if modelID == "" {
		types.WriteError(w, http.StatusBadRequest, types.InvalidRequestError("model ID required"))
		return
	}

	// Find the model across all backends
	models := r.registry.AllModels(req.Context())
	for _, model := range models {
		if model.ID == modelID {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(model)
			return
		}
	}

	types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+modelID))
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	backends := r.registry.AllBackends()

	healthy := 0
	for _, b := range backends {
		if b.IsHealthy() {
			healthy++
		}
	}

	status := struct {
		Status          string `json:"status"`
		BackendsTotal   int    `json:"backends_total"`
		BackendsHealthy int    `json:"backends_healthy"`
		ModelsAvailable int    `json:"models_available"`
	}{
		Status:          "ok",
		BackendsTotal:   len(backends),
		BackendsHealthy: healthy,
		ModelsAvailable: r.registry.ModelCount(),
	}

	if healthy == 0 && len(backends) > 0 {
		status.Status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// readBody reads and returns the request body, allowing it to be read again.
func readBody(req *http.Request) ([]byte, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()
	return body, nil
}
