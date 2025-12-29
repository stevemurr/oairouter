package types

// EmbeddingsRequest represents an OpenAI embeddings request.
type EmbeddingsRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"` // string or []string
	EncodingFormat string `json:"encoding_format,omitempty"` // float or base64
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

// EmbeddingsResponse represents an OpenAI embeddings response.
type EmbeddingsResponse struct {
	Object string          `json:"object"` // list
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  *Usage          `json:"usage"`
}

// EmbeddingData represents a single embedding.
type EmbeddingData struct {
	Object    string    `json:"object"` // embedding
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}
