package oairouter

import (
	"log/slog"
	"net/http"
	"time"
)

// Option configures the Router.
type Option func(*Router) error

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) Option {
	return func(r *Router) error {
		r.logger = l
		return nil
	}
}

// WithHTTPClient sets a custom HTTP client for backends.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Router) error {
		r.httpClient = c
		return nil
	}
}

// WithHealthCheckInterval sets how often to check backend health.
func WithHealthCheckInterval(d time.Duration) Option {
	return func(r *Router) error {
		r.healthCheckInterval = d
		return nil
	}
}

// WithDefaultBackend sets a fallback backend ID when model not found.
func WithDefaultBackend(backendID string) Option {
	return func(r *Router) error {
		r.defaultBackend = backendID
		return nil
	}
}

// WithDiscoverer adds a backend discoverer.
func WithDiscoverer(d Discoverer) Option {
	return func(r *Router) error {
		r.discoverers = append(r.discoverers, d)
		return nil
	}
}
