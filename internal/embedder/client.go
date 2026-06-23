// Package embedder provides a client for OpenAI-compatible embedding APIs.
package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// Client calls an OpenAI-compatible /v1/embeddings endpoint.
type Client struct {
	baseURL   string
	apiKey    string
	model     string
	batchSize int
	http      *http.Client
}

// New creates a new embedding client.
func New(baseURL, apiKey, model string, batchSize, timeoutS int) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		model:     model,
		batchSize: batchSize,
		http:      &http.Client{Timeout: time.Duration(timeoutS) * time.Second},
	}
}

// EmbedBatch embeds all texts, handling batching and retries internally.
// The returned slice preserves input order.
func (c *Client) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	results := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += c.batchSize {
		end := min(start+c.batchSize, len(texts))
		vecs, err := c.embedWithRetry(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embedding batch [%d:%d]: %w", start, end, err)
		}
		copy(results[start:], vecs)
	}
	return results, nil
}

// Dimensions returns the vector size by performing a single test embedding.
// Used for auto-detection when vector_size is not set in config.
func (c *Client) Dimensions(ctx context.Context) (int, error) {
	vecs, err := c.embed(ctx, []string{"probe"})
	if err != nil {
		return 0, fmt.Errorf("probe embedding: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("empty embedding in probe response")
	}
	return len(vecs[0]), nil
}

// ── internals ─────────────────────────────────────────────────────────────────

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (c *Client) embedWithRetry(ctx context.Context, texts []string) ([][]float32, error) {
	const maxAttempts = 3
	var lastErr error

	for attempt := range maxAttempts {
		if attempt > 0 {
			// Exponential backoff with jitter: 1s, 2s, 4s ± 20%
			base := time.Duration(1<<attempt) * time.Second
			jitter := time.Duration(rand.Float64()*0.4*float64(base)) - base/5
			wait := base + jitter
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		vecs, err := c.embed(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result embedResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("API error [%s]: %s", result.Error.Type, result.Error.Message)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("empty data in response")
	}

	// Reconstruct in original order (API may reorder)
	vecs := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("missing embedding for index %d", i)
		}
	}
	return vecs, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
