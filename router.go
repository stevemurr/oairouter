package oairouter

import (
	"context"
	"encoding/json"
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
	sessionAffinity     bool // Enable session affinity via X-Session-ID header

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

// handlerConfig defines the operations for handling a specific API request type.
type handlerConfig[Req any, Resp any] struct {
	getModel     func(*Req) string
	execute      func(Backend, context.Context, *Req) (*Resp, error)
	stream       func(Backend, context.Context, *Req) (<-chan StreamEvent, error)
	isStreaming  func(*Req) bool
	errorContext string
}

// handleAPIRequest is the generic handler for all API request types.
func handleAPIRequest[Req any, Resp any](r *Router, w http.ResponseWriter, req *http.Request, cfg handlerConfig[Req, Resp]) {
	var apiReq Req
	if err := json.NewDecoder(req.Body).Decode(&apiReq); err != nil {
		types.WriteError(w, http.StatusBadRequest, types.InvalidRequestError("invalid request body: "+err.Error()))
		return
	}

	model := cfg.getModel(&apiReq)

	var backend Backend
	var sessionBroken bool

	if r.sessionAffinity {
		// Use session affinity if enabled
		sessionID := req.Header.Get(SessionHeader)
		result, ok := r.registry.LookupByModelWithSession(model, sessionID)
		if ok {
			backend = result.Backend
			sessionBroken = result.SessionBroken
		}
		if !ok {
			if r.defaultBackend != "" {
				backend, ok = r.registry.LookupByID(r.defaultBackend)
			}
			if !ok {
				types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+model))
				return
			}
		}
	} else {
		// Use default lookup
		var ok bool
		backend, ok = r.registry.LookupByModel(model)
		if !ok {
			if r.defaultBackend != "" {
				backend, ok = r.registry.LookupByID(r.defaultBackend)
			}
			if !ok {
				types.WriteError(w, http.StatusNotFound, types.NotFoundError("model not found: "+model))
				return
			}
		}
	}

	// Set session broken header if preferred backend was unhealthy
	if sessionBroken {
		w.Header().Set(SessionBrokenHeader, "true")
	}

	// Handle streaming if supported and requested
	if cfg.stream != nil && cfg.isStreaming != nil && cfg.isStreaming(&apiReq) {
		handleStream(r, w, req, backend, &apiReq, cfg.stream, cfg.errorContext)
		return
	}

	resp, err := cfg.execute(backend, req.Context(), &apiReq)
	if err != nil {
		r.logger.Error(cfg.errorContext+" failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleStream is the generic streaming handler.
func handleStream[Req any](r *Router, w http.ResponseWriter, req *http.Request, backend Backend, apiReq *Req, streamFn func(Backend, context.Context, *Req) (<-chan StreamEvent, error), errorContext string) {
	sse := streaming.NewWriter(w)
	if sse == nil {
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("streaming not supported"))
		return
	}

	events, err := streamFn(backend, req.Context(), apiReq)
	if err != nil {
		r.logger.Error(errorContext+" stream failed", "backend", backend.ID(), "error", err)
		types.WriteError(w, http.StatusInternalServerError, types.ServerError("backend error: "+err.Error()))
		return
	}

	sse.WriteHeaders()

	streamEnded := false
	for event := range events {
		if event.Err != nil {
			r.logger.Error("stream error", "backend", backend.ID(), "error", event.Err)
			break
		}

		if event.Done {
			sse.WriteDone()
			streamEnded = true
			break
		}

		if event.Data != "" {
			if err := sse.WriteData(event.Data); err != nil {
				r.logger.Debug("failed to write SSE data", "error", err)
				break
			}
		}
	}

	if !streamEnded {
		sse.WriteDone()
	}
}

// Handler configurations for each endpoint type
var chatCompletionConfig = handlerConfig[types.ChatCompletionRequest, types.ChatCompletionResponse]{
	getModel: func(r *types.ChatCompletionRequest) string { return r.Model },
	execute: func(b Backend, ctx context.Context, r *types.ChatCompletionRequest) (*types.ChatCompletionResponse, error) {
		return b.ChatCompletion(ctx, r)
	},
	stream: func(b Backend, ctx context.Context, r *types.ChatCompletionRequest) (<-chan StreamEvent, error) {
		return b.ChatCompletionStream(ctx, r)
	},
	isStreaming:  func(r *types.ChatCompletionRequest) bool { return r.Stream },
	errorContext: "chat completion",
}

var completionConfig = handlerConfig[types.CompletionRequest, types.CompletionResponse]{
	getModel: func(r *types.CompletionRequest) string { return r.Model },
	execute: func(b Backend, ctx context.Context, r *types.CompletionRequest) (*types.CompletionResponse, error) {
		return b.Completion(ctx, r)
	},
	stream: func(b Backend, ctx context.Context, r *types.CompletionRequest) (<-chan StreamEvent, error) {
		return b.CompletionStream(ctx, r)
	},
	isStreaming:  func(r *types.CompletionRequest) bool { return r.Stream },
	errorContext: "completion",
}

var embeddingsConfig = handlerConfig[types.EmbeddingsRequest, types.EmbeddingsResponse]{
	getModel: func(r *types.EmbeddingsRequest) string { return r.Model },
	execute: func(b Backend, ctx context.Context, r *types.EmbeddingsRequest) (*types.EmbeddingsResponse, error) {
		return b.Embeddings(ctx, r)
	},
	stream:       nil,
	isStreaming:  nil,
	errorContext: "embeddings",
}

func (r *Router) handleChatCompletions(w http.ResponseWriter, req *http.Request) {
	handleAPIRequest(r, w, req, chatCompletionConfig)
}

func (r *Router) handleCompletions(w http.ResponseWriter, req *http.Request) {
	handleAPIRequest(r, w, req, completionConfig)
}

func (r *Router) handleEmbeddings(w http.ResponseWriter, req *http.Request) {
	handleAPIRequest(r, w, req, embeddingsConfig)
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
