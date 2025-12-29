package types

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APIError represents an OpenAI API error response.
type APIError struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error information.
type ErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

// Common error types
const (
	ErrorTypeInvalidRequest = "invalid_request_error"
	ErrorTypeAuth           = "authentication_error"
	ErrorTypePermission     = "permission_error"
	ErrorTypeNotFound       = "not_found_error"
	ErrorTypeRateLimit      = "rate_limit_error"
	ErrorTypeServer         = "server_error"
)

// NewAPIError creates a new API error.
func NewAPIError(message, errType string, code *string) *APIError {
	return &APIError{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
			Code:    code,
		},
	}
}

// InvalidRequestError creates an invalid request error.
func InvalidRequestError(message string) *APIError {
	return NewAPIError(message, ErrorTypeInvalidRequest, nil)
}

// NotFoundError creates a not found error.
func NotFoundError(message string) *APIError {
	code := "model_not_found"
	return NewAPIError(message, ErrorTypeNotFound, &code)
}

// ServerError creates a server error.
func ServerError(message string) *APIError {
	return NewAPIError(message, ErrorTypeServer, nil)
}

// WriteError writes an API error to the response writer.
func WriteError(w http.ResponseWriter, statusCode int, err *APIError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(err)
}

// RouterError wraps errors with additional context.
type RouterError struct {
	StatusCode int
	APIError   *APIError
	Err        error
}

func (e *RouterError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.APIError.Error.Message, e.Err)
	}
	return e.APIError.Error.Message
}

func (e *RouterError) Unwrap() error {
	return e.Err
}

// NewRouterError creates a new router error.
func NewRouterError(statusCode int, apiErr *APIError, err error) *RouterError {
	return &RouterError{
		StatusCode: statusCode,
		APIError:   apiErr,
		Err:        err,
	}
}
