# oairouter

A modular Go package for routing OpenAI-compatible API requests to multiple LLM backends with auto-discovery.

## Features

- **100% OpenAI API compatible** - Drop-in proxy for any OpenAI client
- **Auto-discovery** - Automatically finds vLLM, Ollama, and llama.cpp Docker containers
- **Model-based routing** - Routes requests to the correct backend based on model name
- **Streaming support** - Full SSE streaming for chat completions
- **Health monitoring** - Background health checks with automatic failover
- **Modular design** - Use as a library in your own projects

## Installation

```bash
go get github.com/stevemurr/oairouter
```

## Quick Start

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/stevemurr/oairouter"
    "github.com/stevemurr/oairouter/discovery"
)

func main() {
    // Create Docker discoverer to find LLM containers
    docker, err := discovery.NewDockerDiscoverer()
    if err != nil {
        log.Fatal(err)
    }

    // Create router
    router, err := oairouter.NewRouter(
        oairouter.WithDiscoverer(docker),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Start discovery and health checks
    ctx := context.Background()
    if err := router.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer router.Stop(ctx)

    // Serve OpenAI-compatible API
    log.Println("Proxy listening on :11434")
    log.Fatal(http.ListenAndServe(":11434", router))
}
```

## Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Chat completions (streaming supported) |
| `/v1/completions` | POST | Legacy completions (streaming supported) |
| `/v1/embeddings` | POST | Text embeddings |
| `/v1/models` | GET | List all available models |
| `/v1/models/{model}` | GET | Get specific model info |
| `/health` | GET | Router health status |

## Usage Examples

### List Models

```bash
curl http://localhost:11434/v1/models
```

### Chat Completion

```bash
curl http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.3-70B-Instruct",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Streaming

```bash
curl http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.3-70B-Instruct",
    "messages": [{"role": "user", "content": "Tell me a story"}],
    "stream": true
  }'
```

### Health Check

```bash
curl http://localhost:11434/health
# {"status":"ok","backends_total":2,"backends_healthy":2,"models_available":3}
```

## Docker Discovery

The Docker discoverer automatically finds containers running known LLM images:

| Image Pattern | Backend Type | Default Port |
|--------------|--------------|--------------|
| `vllm/vllm-openai*` | vllm | 8000 |
| `nvcr.io/nvidia/vllm*` | vllm | 8000 |
| `ollama/ollama*` | ollama | 11434 |
| `ghcr.io/ggerganov/llama.cpp*` | llamacpp | 8080 |

### Custom Image Rules

```go
docker, _ := discovery.NewDockerDiscoverer(
    discovery.WithImageRule(discovery.ImageRule{
        Pattern:     "my-custom-llm",
        BackendType: oairouter.BackendGeneric,
        DefaultPort: 8080,
    }),
)
```

### Using Existing Docker Client

```go
import "github.com/docker/docker/client"

dockerClient, _ := client.NewClientWithOpts(client.FromEnv)

docker, _ := discovery.NewDockerDiscoverer(
    discovery.WithDockerClient(dockerClient),
)
```

## Manual Backend Registration

```go
import "github.com/stevemurr/oairouter/backends"

// Add a backend manually
backend, _ := backends.NewGenericBackend(
    "my-llm",
    "http://192.168.1.100:8000",
)
router.AddBackend(ctx, backend)

// Remove a backend
router.RemoveBackend("my-llm")
```

## Configuration Options

```go
router, _ := oairouter.NewRouter(
    // Add discoverers
    oairouter.WithDiscoverer(dockerDiscoverer),

    // Custom logger
    oairouter.WithLogger(slog.Default()),

    // Custom HTTP client for backends
    oairouter.WithHTTPClient(&http.Client{Timeout: 5 * time.Minute}),

    // Health check interval
    oairouter.WithHealthCheckInterval(30 * time.Second),

    // Default backend when model not found
    oairouter.WithDefaultBackend("fallback-llm"),
)
```

## Package Structure

```
oairouter/
├── router.go           # Main Router, http.Handler
├── backend.go          # Backend interface
├── registry.go         # Model-to-backend routing
├── options.go          # Functional options
├── types/
│   ├── chat.go         # ChatCompletion types
│   ├── completion.go   # Completion types
│   ├── embeddings.go   # Embedding types
│   ├── models.go       # Model types
│   └── errors.go       # Error types
├── backends/
│   └── generic.go      # Generic OpenAI-compatible backend
├── discovery/
│   ├── discoverer.go   # Discoverer interface
│   └── docker.go       # Docker container discovery
└── streaming/
    └── sse.go          # SSE utilities
```

## License

MIT
