package oairouter

import (
	"context"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/oairouter/types"
)

// mockBackend is a simple backend for testing
type mockBackend struct {
	id      string
	healthy atomic.Bool
}

func newMockBackend(id string, healthy bool) *mockBackend {
	b := &mockBackend{id: id}
	b.healthy.Store(healthy)
	return b
}

func (b *mockBackend) ID() string                        { return b.id }
func (b *mockBackend) Type() BackendType                 { return BackendGeneric }
func (b *mockBackend) BaseURL() *url.URL                 { return &url.URL{Scheme: "http", Host: "localhost:8080"} }
func (b *mockBackend) Models(ctx context.Context) ([]types.Model, error) {
	return []types.Model{{ID: "test-model", Object: "model"}}, nil
}
func (b *mockBackend) HealthCheck(ctx context.Context) error { return nil }
func (b *mockBackend) IsHealthy() bool                       { return b.healthy.Load() }
func (b *mockBackend) SetHealthy(h bool)                     { b.healthy.Store(h) }
func (b *mockBackend) ChatCompletion(ctx context.Context, req *types.ChatCompletionRequest) (*types.ChatCompletionResponse, error) {
	return nil, nil
}
func (b *mockBackend) ChatCompletionStream(ctx context.Context, req *types.ChatCompletionRequest) (<-chan StreamEvent, error) {
	return nil, nil
}
func (b *mockBackend) Completion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	return nil, nil
}
func (b *mockBackend) CompletionStream(ctx context.Context, req *types.CompletionRequest) (<-chan StreamEvent, error) {
	return nil, nil
}
func (b *mockBackend) Embeddings(ctx context.Context, req *types.EmbeddingsRequest) (*types.EmbeddingsResponse, error) {
	return nil, nil
}

func TestLookupByModelWithSession_ConsistentRouting(t *testing.T) {
	// Test that the same session ID consistently routes to the same backend
	r := NewBackendRegistry()
	ctx := context.Background()

	// Register multiple backends for the same model
	backends := []*mockBackend{
		newMockBackend("backend-a", true),
		newMockBackend("backend-b", true),
		newMockBackend("backend-c", true),
	}

	for _, b := range backends {
		r.Register(ctx, b)
	}

	sessionID := "session-123"
	modelID := "test-model"

	// Make multiple requests with the same session ID
	var firstBackendID string
	for i := 0; i < 10; i++ {
		result, ok := r.LookupByModelWithSession(modelID, sessionID)
		if !ok {
			t.Fatal("expected to find backend")
		}
		if firstBackendID == "" {
			firstBackendID = result.Backend.ID()
		} else if result.Backend.ID() != firstBackendID {
			t.Errorf("session affinity broken: got %s, want %s", result.Backend.ID(), firstBackendID)
		}
		if result.SessionBroken {
			t.Error("expected SessionBroken to be false")
		}
	}
}

func TestLookupByModelWithSession_DifferentSessionsDistribute(t *testing.T) {
	// Test that different session IDs distribute across backends
	r := NewBackendRegistry()
	ctx := context.Background()

	backends := []*mockBackend{
		newMockBackend("backend-a", true),
		newMockBackend("backend-b", true),
		newMockBackend("backend-c", true),
	}

	for _, b := range backends {
		r.Register(ctx, b)
	}

	modelID := "test-model"
	backendHits := make(map[string]int)

	// Use many different session IDs
	for i := 0; i < 100; i++ {
		sessionID := "session-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		result, ok := r.LookupByModelWithSession(modelID, sessionID)
		if !ok {
			t.Fatal("expected to find backend")
		}
		backendHits[result.Backend.ID()]++
	}

	// Check that at least 2 backends were hit (distribution is working)
	if len(backendHits) < 2 {
		t.Errorf("expected distribution across multiple backends, got %d", len(backendHits))
	}
}

func TestLookupByModelWithSession_NoSessionFallsBackToFirstHealthy(t *testing.T) {
	r := NewBackendRegistry()
	ctx := context.Background()

	backends := []*mockBackend{
		newMockBackend("backend-a", false), // unhealthy
		newMockBackend("backend-b", true),  // healthy
		newMockBackend("backend-c", true),  // healthy
	}

	for _, b := range backends {
		r.Register(ctx, b)
	}

	modelID := "test-model"

	// Empty session should use first-healthy behavior
	result, ok := r.LookupByModelWithSession(modelID, "")
	if !ok {
		t.Fatal("expected to find backend")
	}

	// Should get a healthy backend
	if !result.Backend.IsHealthy() {
		t.Error("expected to get a healthy backend when no session")
	}
	if result.SessionBroken {
		t.Error("expected SessionBroken to be false when no session")
	}
}

func TestLookupByModelWithSession_FallbackWhenPreferredUnhealthy(t *testing.T) {
	r := NewBackendRegistry()
	ctx := context.Background()

	backends := []*mockBackend{
		newMockBackend("backend-a", true),
		newMockBackend("backend-b", true),
		newMockBackend("backend-c", true),
	}

	for _, b := range backends {
		r.Register(ctx, b)
	}

	modelID := "test-model"
	sessionID := "session-xyz"

	// Find which backend this session maps to
	result1, ok := r.LookupByModelWithSession(modelID, sessionID)
	if !ok {
		t.Fatal("expected to find backend")
	}
	preferredBackend := result1.Backend.ID()

	// Make the preferred backend unhealthy
	for _, b := range backends {
		if b.ID() == preferredBackend {
			b.SetHealthy(false)
			break
		}
	}

	// Now lookup should fall back to another backend and set SessionBroken
	result2, ok := r.LookupByModelWithSession(modelID, sessionID)
	if !ok {
		t.Fatal("expected to find backend on fallback")
	}

	if result2.Backend.ID() == preferredBackend {
		t.Error("expected to get a different backend after preferred became unhealthy")
	}
	if !result2.SessionBroken {
		t.Error("expected SessionBroken to be true when preferred backend was unhealthy")
	}
	if !result2.Backend.IsHealthy() {
		t.Error("expected fallback backend to be healthy")
	}
}

func TestLookupByModelWithSession_AllBackendsUnhealthy(t *testing.T) {
	r := NewBackendRegistry()
	ctx := context.Background()

	backends := []*mockBackend{
		newMockBackend("backend-a", false),
		newMockBackend("backend-b", false),
	}

	for _, b := range backends {
		r.Register(ctx, b)
	}

	modelID := "test-model"
	sessionID := "session-123"

	// Should still return a backend (preferred one based on hash), but mark session as broken
	result, ok := r.LookupByModelWithSession(modelID, sessionID)
	if !ok {
		t.Fatal("expected to find backend even when all unhealthy")
	}
	if result.SessionBroken != true {
		t.Error("expected SessionBroken to be true when all backends unhealthy")
	}
}

func TestLookupByModelWithSession_ModelNotFound(t *testing.T) {
	r := NewBackendRegistry()
	ctx := context.Background()

	b := newMockBackend("backend-a", true)
	r.Register(ctx, b)

	// Try to look up a model that doesn't exist
	_, ok := r.LookupByModelWithSession("nonexistent-model", "session-123")
	if ok {
		t.Error("expected lookup to fail for nonexistent model")
	}
}

func TestHashSessionToIndex_Deterministic(t *testing.T) {
	// Test that the same session ID always produces the same index
	sessionID := "test-session-id"
	count := 5

	firstResult := hashSessionToIndex(sessionID, count)
	for i := 0; i < 100; i++ {
		result := hashSessionToIndex(sessionID, count)
		if result != firstResult {
			t.Errorf("hash not deterministic: got %d, want %d", result, firstResult)
		}
	}

	// Test that result is within bounds
	if firstResult < 0 || firstResult >= count {
		t.Errorf("hash result out of bounds: %d (count=%d)", firstResult, count)
	}
}

func TestHashSessionToIndex_DifferentSessions(t *testing.T) {
	// Test that different session IDs produce different indices (at least some of the time)
	count := 10
	indices := make(map[int]bool)

	for i := 0; i < 100; i++ {
		sessionID := "session-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		idx := hashSessionToIndex(sessionID, count)
		indices[idx] = true
	}

	// We should hit multiple different indices
	if len(indices) < 3 {
		t.Errorf("expected more distribution, only got %d different indices", len(indices))
	}
}
