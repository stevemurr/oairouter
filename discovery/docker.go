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

// ImageRule maps Docker image patterns to backend types.
type ImageRule struct {
	Pattern     string
	BackendType oairouter.BackendType
	PortLabel   string // Docker label containing port (optional)
	ModelLabel  string // Docker label containing model ID (optional)
	DefaultPort int    // Default port if not specified
}

// DefaultImageRules are built-in rules for common LLM backends.
var DefaultImageRules = []ImageRule{
	{
		Pattern:     "vllm/vllm-openai",
		BackendType: oairouter.BackendVLLM,
		PortLabel:   "vllm-manager.port",
		ModelLabel:  "vllm-manager.model",
		DefaultPort: 8000,
	},
	{
		Pattern:     "nvcr.io/nvidia/vllm",
		BackendType: oairouter.BackendVLLM,
		PortLabel:   "vllm-manager.port",
		ModelLabel:  "vllm-manager.model",
		DefaultPort: 8000,
	},
	{
		Pattern:     "ollama/ollama",
		BackendType: oairouter.BackendOllama,
		DefaultPort: 11434,
	},
	{
		Pattern:     "ghcr.io/ggerganov/llama.cpp",
		BackendType: oairouter.BackendLlamaCpp,
		DefaultPort: 8080,
	},
}

// DockerDiscoverer finds LLM backends running in Docker containers.
type DockerDiscoverer struct {
	client     *client.Client
	imageRules []ImageRule
	ownClient  bool
}

// DockerOption configures the Docker discoverer.
type DockerOption func(*DockerDiscoverer)

// WithImageRule adds a custom image rule.
func WithImageRule(rule ImageRule) DockerOption {
	return func(d *DockerDiscoverer) {
		d.imageRules = append(d.imageRules, rule)
	}
}

// WithDockerClient uses an existing Docker client.
func WithDockerClient(c *client.Client) DockerOption {
	return func(d *DockerDiscoverer) {
		d.client = c
		d.ownClient = false
	}
}

// NewDockerDiscoverer creates a new Docker discoverer.
func NewDockerDiscoverer(opts ...DockerOption) (*DockerDiscoverer, error) {
	d := &DockerDiscoverer{
		imageRules: DefaultImageRules,
		ownClient:  true,
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
	// Match against image rules
	var matchedRule *ImageRule
	for i := range d.imageRules {
		rule := &d.imageRules[i]
		if matchesPattern(c.Image, rule.Pattern) {
			matchedRule = rule
			break
		}
	}

	if matchedRule == nil {
		return nil, false
	}

	// Extract port
	port := matchedRule.DefaultPort
	if matchedRule.PortLabel != "" {
		if portStr, ok := c.Labels[matchedRule.PortLabel]; ok {
			if p, err := strconv.Atoi(portStr); err == nil {
				port = p
			}
		}
	}

	// Build backend ID
	name := c.ID[:12]
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	id := fmt.Sprintf("%s-%s", matchedRule.BackendType, name)

	// Build URL
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	backend, err := backends.NewGenericBackend(
		id,
		baseURL,
		backends.WithBackendType(matchedRule.BackendType),
	)
	if err != nil {
		return nil, false
	}

	return backend, true
}

// matchesPattern checks if an image name matches a pattern.
// Patterns can use * as a wildcard.
func matchesPattern(image, pattern string) bool {
	// Simple prefix match for now
	// "vllm/vllm-openai" matches "vllm/vllm-openai:latest"
	if strings.HasPrefix(image, pattern) {
		return true
	}

	// Handle wildcard patterns
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 {
			return strings.HasPrefix(image, parts[0]) && strings.HasSuffix(image, parts[1])
		}
	}

	return false
}

// Close closes the Docker client if owned by this discoverer.
func (d *DockerDiscoverer) Close() error {
	if d.ownClient && d.client != nil {
		return d.client.Close()
	}
	return nil
}
