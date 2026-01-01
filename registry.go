package oairouter

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"

	"github.com/stevemurr/oairouter/types"
)

// SessionHeader is the HTTP header used for session affinity.
const SessionHeader = "X-Session-ID"

// SessionBrokenHeader is set in responses when session affinity couldn't be maintained.
const SessionBrokenHeader = "X-Session-Broken"

// BackendRegistry manages model-to-backend routing.
type BackendRegistry struct {
	mu       sync.RWMutex
	backends map[string]Backend   // backendID -> Backend
	models   map[string][]string  // modelID -> []backendID (multiple backends may serve same model)
}

// NewBackendRegistry creates a new backend registry.
func NewBackendRegistry() *BackendRegistry {
	return &BackendRegistry{
		backends: make(map[string]Backend),
		models:   make(map[string][]string),
	}
}

// Register adds a backend and indexes its models.
func (r *BackendRegistry) Register(ctx context.Context, b Backend) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.backends[b.ID()] = b

	// Fetch and index models
	models, err := b.Models(ctx)
	if err != nil {
		// Backend registered but models not available yet
		return nil
	}

	for _, model := range models {
		r.addModelMapping(model.ID, b.ID())
	}

	return nil
}

// Unregister removes a backend and its model mappings.
func (r *BackendRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.backends, id)

	// Remove model mappings for this backend
	for modelID, backendIDs := range r.models {
		filtered := make([]string, 0, len(backendIDs))
		for _, bid := range backendIDs {
			if bid != id {
				filtered = append(filtered, bid)
			}
		}
		if len(filtered) == 0 {
			delete(r.models, modelID)
		} else {
			r.models[modelID] = filtered
		}
	}
}

// addModelMapping adds a model -> backend mapping (must hold lock).
func (r *BackendRegistry) addModelMapping(modelID, backendID string) {
	backends := r.models[modelID]
	// Check if already mapped
	for _, bid := range backends {
		if bid == backendID {
			return
		}
	}
	r.models[modelID] = append(backends, backendID)
}

// LookupByModel finds the first healthy backend serving a specific model.
func (r *BackendRegistry) LookupByModel(modelID string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	backendIDs, ok := r.models[modelID]
	if !ok || len(backendIDs) == 0 {
		return nil, false
	}

	// First-available: return the first healthy backend
	for _, bid := range backendIDs {
		backend, ok := r.backends[bid]
		if ok && backend.IsHealthy() {
			return backend, true
		}
	}

	// No healthy backend found, return first one anyway (caller can handle unhealthy)
	if backend, ok := r.backends[backendIDs[0]]; ok {
		return backend, true
	}

	return nil, false
}

// LookupResult contains the backend lookup result with session affinity metadata.
type LookupResult struct {
	Backend       Backend
	SessionBroken bool // True if preferred backend was unhealthy and fallback was used
}

// LookupByModelWithSession finds a backend using session affinity via consistent hashing.
// If sessionID is empty, falls back to first-healthy selection (LookupByModel behavior).
// If the preferred backend (based on session hash) is unhealthy, falls back to another
// healthy backend and sets SessionBroken=true in the result.
func (r *BackendRegistry) LookupByModelWithSession(modelID, sessionID string) (LookupResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	backendIDs, ok := r.models[modelID]
	if !ok || len(backendIDs) == 0 {
		return LookupResult{}, false
	}

	// No session - use first-healthy selection
	if sessionID == "" {
		for _, bid := range backendIDs {
			backend, ok := r.backends[bid]
			if ok && backend.IsHealthy() {
				return LookupResult{Backend: backend, SessionBroken: false}, true
			}
		}
		// No healthy backend, return first anyway
		if backend, ok := r.backends[backendIDs[0]]; ok {
			return LookupResult{Backend: backend, SessionBroken: false}, true
		}
		return LookupResult{}, false
	}

	// Get healthy backends for this model, sorted by ID for consistent ordering
	healthyBackends := make([]Backend, 0, len(backendIDs))
	var allBackends []Backend
	for _, bid := range backendIDs {
		backend, ok := r.backends[bid]
		if !ok {
			continue
		}
		allBackends = append(allBackends, backend)
		if backend.IsHealthy() {
			healthyBackends = append(healthyBackends, backend)
		}
	}

	// Sort for consistent ordering (backends may be registered in different order)
	sort.Slice(allBackends, func(i, j int) bool {
		return allBackends[i].ID() < allBackends[j].ID()
	})
	sort.Slice(healthyBackends, func(i, j int) bool {
		return healthyBackends[i].ID() < healthyBackends[j].ID()
	})

	if len(allBackends) == 0 {
		return LookupResult{}, false
	}

	// Compute preferred backend using consistent hashing over ALL backends
	preferredIndex := hashSessionToIndex(sessionID, len(allBackends))
	preferredBackend := allBackends[preferredIndex]

	// If preferred backend is healthy, use it
	if preferredBackend.IsHealthy() {
		return LookupResult{Backend: preferredBackend, SessionBroken: false}, true
	}

	// Preferred backend unhealthy - fall back to a healthy one
	if len(healthyBackends) > 0 {
		// Use consistent hashing on healthy backends as fallback
		fallbackIndex := hashSessionToIndex(sessionID, len(healthyBackends))
		return LookupResult{Backend: healthyBackends[fallbackIndex], SessionBroken: true}, true
	}

	// No healthy backends - return preferred (unhealthy) backend anyway
	return LookupResult{Backend: preferredBackend, SessionBroken: true}, true
}

// hashSessionToIndex uses FNV-1a hashing to consistently map a session ID to an index.
func hashSessionToIndex(sessionID string, count int) int {
	h := fnv.New32a()
	h.Write([]byte(sessionID))
	return int(h.Sum32() % uint32(count))
}

// LookupByID finds a backend by its ID.
func (r *BackendRegistry) LookupByID(id string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b, ok := r.backends[id]
	return b, ok
}

// AllBackends returns all registered backends.
func (r *BackendRegistry) AllBackends() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	backends := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		backends = append(backends, b)
	}
	return backends
}

// AllModels returns all available models across all backends.
// It also updates the model index to ensure lookups work.
func (r *BackendRegistry) AllModels(ctx context.Context) []types.Model {
	r.mu.Lock()
	defer r.mu.Unlock()

	var allModels []types.Model
	seen := make(map[string]bool)

	for _, backend := range r.backends {
		models, err := backend.Models(ctx)
		if err != nil {
			continue
		}
		for _, model := range models {
			// Update model index
			r.addModelMapping(model.ID, backend.ID())

			if !seen[model.ID] {
				seen[model.ID] = true
				allModels = append(allModels, model)
			}
		}
	}

	return allModels
}

// RefreshModels updates the model index for a backend.
func (r *BackendRegistry) RefreshModels(ctx context.Context, backendID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	backend, ok := r.backends[backendID]
	if !ok {
		return fmt.Errorf("backend not found: %s", backendID)
	}

	// Remove existing mappings for this backend
	for modelID, backendIDs := range r.models {
		filtered := make([]string, 0, len(backendIDs))
		for _, bid := range backendIDs {
			if bid != backendID {
				filtered = append(filtered, bid)
			}
		}
		if len(filtered) == 0 {
			delete(r.models, modelID)
		} else {
			r.models[modelID] = filtered
		}
	}

	// Fetch and re-index models
	models, err := backend.Models(ctx)
	if err != nil {
		return err
	}

	for _, model := range models {
		r.addModelMapping(model.ID, backendID)
	}

	return nil
}

// Count returns the number of registered backends.
func (r *BackendRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.backends)
}

// ModelCount returns the number of unique models.
func (r *BackendRegistry) ModelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}
