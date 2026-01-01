package discovery

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/stevemurr/oairouter"
)

func TestDefaultPortForType(t *testing.T) {
	tests := []struct {
		backendType oairouter.BackendType
		expected    int
	}{
		{oairouter.BackendVLLM, 8000},
		{oairouter.BackendOllama, 11434},
		{oairouter.BackendLlamaCpp, 8080},
		{oairouter.BackendLMStudio, 1234},
		{oairouter.BackendGeneric, 8080},
		{"unknown", 8080},
	}

	for _, tt := range tests {
		t.Run(string(tt.backendType), func(t *testing.T) {
			got := defaultPortForType(tt.backendType)
			if got != tt.expected {
				t.Errorf("defaultPortForType(%s) = %d, want %d", tt.backendType, got, tt.expected)
			}
		})
	}
}

func TestContainerToBackend(t *testing.T) {
	cfg := LabelConfig{
		Prefix:         "oairouter.",
		EnabledKey:     "enabled",
		BackendTypeKey: "backend",
		PortKey:        "port",
		ModelKey:       "model",
		URLKey:         "url",
		DefaultHost:    "localhost",
	}

	d := &DockerDiscoverer{labels: cfg}

	tests := []struct {
		name        string
		container   types.Container
		wantBackend bool
		wantID      string
		wantURL     string
	}{
		{
			name: "enabled with all labels",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/my-vllm"},
				Labels: map[string]string{
					"oairouter.enabled": "true",
					"oairouter.backend": "vllm",
					"oairouter.port":    "9000",
				},
			},
			wantBackend: true,
			wantID:      "vllm-my-vllm",
			wantURL:     "http://localhost:9000",
		},
		{
			name: "enabled with URL override",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/my-service"},
				Labels: map[string]string{
					"oairouter.enabled": "true",
					"oairouter.backend": "vllm",
					"oairouter.url":     "http://vllm-service:8000",
				},
			},
			wantBackend: true,
			wantID:      "vllm-my-service",
			wantURL:     "http://vllm-service:8000",
		},
		{
			name: "enabled with minimal labels uses defaults",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/my-generic"},
				Labels: map[string]string{
					"oairouter.enabled": "true",
				},
			},
			wantBackend: true,
			wantID:      "generic-my-generic",
			wantURL:     "http://localhost:8080",
		},
		{
			name: "enabled with backend type uses default port for type",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/my-ollama"},
				Labels: map[string]string{
					"oairouter.enabled": "true",
					"oairouter.backend": "ollama",
				},
			},
			wantBackend: true,
			wantID:      "ollama-my-ollama",
			wantURL:     "http://localhost:11434",
		},
		{
			name: "not enabled",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/not-managed"},
				Labels: map[string]string{
					"oairouter.backend": "vllm",
				},
			},
			wantBackend: false,
		},
		{
			name: "enabled set to false",
			container: types.Container{
				ID:    "abc123def456",
				Names: []string{"/disabled"},
				Labels: map[string]string{
					"oairouter.enabled": "false",
					"oairouter.backend": "vllm",
				},
			},
			wantBackend: false,
		},
		{
			name: "no labels",
			container: types.Container{
				ID:     "abc123def456",
				Names:  []string{"/no-labels"},
				Labels: map[string]string{},
			},
			wantBackend: false,
		},
		{
			name: "uses container ID when no name",
			container: types.Container{
				ID:     "abc123def456789",
				Names:  []string{},
				Labels: map[string]string{
					"oairouter.enabled": "true",
				},
			},
			wantBackend: true,
			wantID:      "generic-abc123def456",
			wantURL:     "http://localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, ok := d.containerToBackend(tt.container)
			if ok != tt.wantBackend {
				t.Errorf("containerToBackend() ok = %v, want %v", ok, tt.wantBackend)
				return
			}
			if !ok {
				return
			}
			if backend.ID() != tt.wantID {
				t.Errorf("backend.ID() = %s, want %s", backend.ID(), tt.wantID)
			}
			if backend.BaseURL().String() != tt.wantURL {
				t.Errorf("backend.BaseURL() = %s, want %s", backend.BaseURL().String(), tt.wantURL)
			}
		})
	}
}

func TestCustomLabelPrefix(t *testing.T) {
	cfg := LabelConfig{
		Prefix:         "llm.manager/",
		EnabledKey:     "enabled",
		BackendTypeKey: "backend",
		PortKey:        "port",
		ModelKey:       "model",
		URLKey:         "url",
		DefaultHost:    "host.docker.internal",
	}

	d := &DockerDiscoverer{labels: cfg}

	container := types.Container{
		ID:    "abc123def456",
		Names: []string{"/my-vllm"},
		Labels: map[string]string{
			"llm.manager/enabled": "true",
			"llm.manager/backend": "vllm",
			"llm.manager/port":    "8000",
		},
	}

	backend, ok := d.containerToBackend(container)
	if !ok {
		t.Fatal("expected backend to be discovered")
	}

	if backend.ID() != "vllm-my-vllm" {
		t.Errorf("backend.ID() = %s, want vllm-my-vllm", backend.ID())
	}

	expectedURL := "http://host.docker.internal:8000"
	if backend.BaseURL().String() != expectedURL {
		t.Errorf("backend.BaseURL() = %s, want %s", backend.BaseURL().String(), expectedURL)
	}
}

func TestGetBaseURL(t *testing.T) {
	cfg := LabelConfig{
		Prefix:         "oairouter.",
		EnabledKey:     "enabled",
		BackendTypeKey: "backend",
		PortKey:        "port",
		URLKey:         "url",
		DefaultHost:    "localhost",
	}

	d := &DockerDiscoverer{labels: cfg}

	tests := []struct {
		name        string
		labels      map[string]string
		backendType oairouter.BackendType
		expected    string
	}{
		{
			name: "URL override takes precedence",
			labels: map[string]string{
				"oairouter.url":  "http://custom:9999",
				"oairouter.port": "8000",
			},
			backendType: oairouter.BackendVLLM,
			expected:    "http://custom:9999",
		},
		{
			name: "port label overrides default",
			labels: map[string]string{
				"oairouter.port": "9000",
			},
			backendType: oairouter.BackendVLLM,
			expected:    "http://localhost:9000",
		},
		{
			name:        "uses default port for backend type",
			labels:      map[string]string{},
			backendType: oairouter.BackendOllama,
			expected:    "http://localhost:11434",
		},
		{
			name:        "generic uses default 8080",
			labels:      map[string]string{},
			backendType: oairouter.BackendGeneric,
			expected:    "http://localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := types.Container{Labels: tt.labels}
			got := d.getBaseURL(container, tt.backendType)
			if got != tt.expected {
				t.Errorf("getBaseURL() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestContainerName(t *testing.T) {
	d := &DockerDiscoverer{}

	tests := []struct {
		name      string
		container types.Container
		expected  string
	}{
		{
			name: "strips leading slash from name",
			container: types.Container{
				ID:    "abc123def456789",
				Names: []string{"/my-container"},
			},
			expected: "my-container",
		},
		{
			name: "uses first 12 chars of ID when no name",
			container: types.Container{
				ID:    "abc123def456789xyz",
				Names: []string{},
			},
			expected: "abc123def456",
		},
		{
			name: "uses first name when multiple",
			container: types.Container{
				ID:    "abc123def456789",
				Names: []string{"/first", "/second"},
			},
			expected: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.containerName(tt.container)
			if got != tt.expected {
				t.Errorf("containerName() = %s, want %s", got, tt.expected)
			}
		})
	}
}
