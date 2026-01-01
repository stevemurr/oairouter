package discovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/stevemurr/oairouter"
	"github.com/stevemurr/oairouter/backends"
)

// LabelConfig defines the label schema for container discovery.
// Containers must have the enabled label set to "true" to be discovered.
type LabelConfig struct {
	Prefix         string // Label prefix, e.g., "oairouter." or "llm.manager/"
	EnabledKey     string // Key for enabled flag, e.g., "enabled"
	BackendTypeKey string // Key for backend type, e.g., "backend"
	PortKey        string // Key for port, e.g., "port"
	ModelKey       string // Key for model ID, e.g., "model"
	URLKey         string // Key for full URL override, e.g., "url"
	DefaultHost    string // Default host when URL not specified, e.g., "localhost"
}

// DockerDiscoverer finds LLM backends running in Docker containers.
// Containers opt-in to discovery by setting the enabled label to "true".
type DockerDiscoverer struct {
	client    *client.Client
	labels    LabelConfig
	ownClient bool
}

// DockerOption configures the Docker discoverer.
type DockerOption func(*DockerDiscoverer)

// WithDockerClient uses an existing Docker client (useful for testing).
func WithDockerClient(c *client.Client) DockerOption {
	return func(d *DockerDiscoverer) {
		d.client = c
		d.ownClient = false
	}
}

// NewDockerDiscoverer creates a new Docker discoverer with the given label configuration.
// Containers must have the label "{Prefix}{EnabledKey}" set to "true" to be discovered.
func NewDockerDiscoverer(labels LabelConfig, opts ...DockerOption) (*DockerDiscoverer, error) {
	d := &DockerDiscoverer{
		labels:    labels,
		ownClient: true,
	}

	for _, opt := range opts {
		opt(d)
	}

	if d.client == nil {
		c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %w", err)
		}
		d.client = c
	}

	return d, nil
}

func (d *DockerDiscoverer) Name() string {
	return "docker"
}

func (d *DockerDiscoverer) Discover(ctx context.Context) ([]oairouter.Backend, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All: false, // Only running containers
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var foundBackends []oairouter.Backend

	for _, c := range containers {
		backend, ok := d.containerToBackend(c)
		if ok {
			foundBackends = append(foundBackends, backend)
		}
	}

	return foundBackends, nil
}

func (d *DockerDiscoverer) Watch(ctx context.Context) (<-chan oairouter.DiscoveryEvent, error) {
	eventsChan := make(chan oairouter.DiscoveryEvent, 10)

	// Subscribe to Docker events
	eventFilter := filters.NewArgs()
	eventFilter.Add("type", "container")
	eventFilter.Add("event", "start")
	eventFilter.Add("event", "stop")
	eventFilter.Add("event", "die")

	dockerEvents, errChan := d.client.Events(ctx, events.ListOptions{
		Filters: eventFilter,
	})

	go func() {
		defer close(eventsChan)

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errChan:
				if err != nil {
					return
				}
			case event := <-dockerEvents:
				d.handleDockerEvent(ctx, event, eventsChan)
			}
		}
	}()

	return eventsChan, nil
}

func (d *DockerDiscoverer) handleDockerEvent(ctx context.Context, event events.Message, out chan<- oairouter.DiscoveryEvent) {
	// Get container details
	containerJSON, err := d.client.ContainerInspect(ctx, event.Actor.ID)
	if err != nil {
		return
	}

	// Convert to types.Container-like structure for matching
	c := types.Container{
		ID:     containerJSON.ID,
		Names:  []string{containerJSON.Name},
		Image:  containerJSON.Config.Image,
		Labels: containerJSON.Config.Labels,
		State:  containerJSON.State.Status,
	}

	backend, ok := d.containerToBackend(c)
	if !ok {
		return
	}

	var eventType oairouter.EventType
	switch string(event.Action) {
	case "start":
		eventType = oairouter.EventAdded
	case "stop", "die":
		eventType = oairouter.EventRemoved
	default:
		return
	}

	select {
	case out <- oairouter.DiscoveryEvent{Type: eventType, Backend: backend}:
	default:
		// Channel full, skip event
	}
}

func (d *DockerDiscoverer) containerToBackend(c types.Container) (oairouter.Backend, bool) {
	// 1. Check enabled label (required)
	enabledLabel := d.labels.Prefix + d.labels.EnabledKey
	if c.Labels[enabledLabel] != "true" {
		return nil, false
	}

	// 2. Get backend type from label (default: generic)
	backendType := oairouter.BackendGeneric
	if d.labels.BackendTypeKey != "" {
		if typeStr := c.Labels[d.labels.Prefix+d.labels.BackendTypeKey]; typeStr != "" {
			backendType = oairouter.BackendType(typeStr)
		}
	}

	// 3. Get base URL
	baseURL := d.getBaseURL(c, backendType)

	// 4. Build backend ID from container name
	name := d.containerName(c)
	id := fmt.Sprintf("%s-%s", backendType, name)

	// 5. Create backend
	backend, err := backends.NewGenericBackend(
		id,
		baseURL,
		backends.WithBackendType(backendType),
	)
	if err != nil {
		return nil, false
	}

	return backend, true
}

// getBaseURL returns the base URL for the container.
// If URLKey label is set, uses that directly. Otherwise constructs from DefaultHost + port.
func (d *DockerDiscoverer) getBaseURL(c types.Container, backendType oairouter.BackendType) string {
	// Check for full URL override
	if d.labels.URLKey != "" {
		if url := c.Labels[d.labels.Prefix+d.labels.URLKey]; url != "" {
			return url
		}
	}

	// Construct from DefaultHost + port
	port := defaultPortForType(backendType)
	if d.labels.PortKey != "" {
		if portStr := c.Labels[d.labels.Prefix+d.labels.PortKey]; portStr != "" {
			if p, err := strconv.Atoi(portStr); err == nil {
				port = p
			}
		}
	}

	return fmt.Sprintf("http://%s:%d", d.labels.DefaultHost, port)
}

// containerName extracts a clean name from the container.
func (d *DockerDiscoverer) containerName(c types.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID[:12]
}

// defaultPortForType returns the standard port for a backend type.
func defaultPortForType(t oairouter.BackendType) int {
	switch t {
	case oairouter.BackendVLLM:
		return 8000
	case oairouter.BackendOllama:
		return 11434
	case oairouter.BackendLlamaCpp:
		return 8080
	case oairouter.BackendLMStudio:
		return 1234
	default:
		return 8080
	}
}

// Close closes the Docker client if owned by this discoverer.
func (d *DockerDiscoverer) Close() error {
	if d.ownClient && d.client != nil {
		return d.client.Close()
	}
	return nil
}
