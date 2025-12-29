package discovery

import (
	"context"

	"github.com/stevemurr/oairouter"
)

// Discoverer finds and monitors LLM backends.
// This interface is also defined in the main oairouter package.
type Discoverer interface {
	Name() string
	Discover(ctx context.Context) ([]oairouter.Backend, error)
	Watch(ctx context.Context) (<-chan oairouter.DiscoveryEvent, error)
}
