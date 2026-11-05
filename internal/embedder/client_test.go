package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	body embedRequest
	auth string
	path string
}

type fakeAPI struct {
	mu    sync.Mutex
	calls []recordedRequest
}

type handlerFunc func(w http.ResponseWriter, call int, req embedRequest)

func newFakeAPI(t *testing.T, handler handlerFunc) (*fakeAPI, *Client) {
	t.Helper()
	api := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		api.mu.Lock()
		call := len(api.calls)
		api.calls = append(api.calls, recordedRequest{body: req, auth: r.Header.Get("Authorization"), path: r.URL.Path})
		api.mu.Unlock()
		handler(w, call, req)
	}))
	t.Cleanup(srv.Close)
	return api, New(srv.URL, "test-key", "test-model", 32, 5)
}

func (api *fakeAPI) snapshot() []recordedRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]recordedRequest(nil), api.calls...)
}

func okEmbedding(embedding string) handlerFunc {
	return func(w http.ResponseWriter, _ int, _ embedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[` + embedding + `]}]}`))
	}
}

func TestNewTrimsTrailingSlashes(t *testing.T) {
	c := New("http://api.example/v1///", "k", "m", 8, 5)
	if c.baseURL != "http://api.example/v1" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://api.example/v1")
	}
	if c.apiKey != "k" || c.model != "m" || c.batchSize != 8 {
		t.Errorf("client fields = %+v, want key k model m batch 8", c)
	}
	c = New("http://api.example", "k", "m", 8, 5)
	if c.baseURL != "http://api.example" {
		t.Errorf("baseURL = %q, want it unchanged without trailing slash", c.baseURL)
	}
}

func TestEmbedBatchEmptyInputMakesNoRequest(t *testing.T) {
	api, c := newFakeAPI(t, func(http.ResponseWriter, int, embedRequest) {
		t.Error("server handler invoked for empty input")
	})
	for _, texts := range [][]string{nil, {}} {
		vecs, err := c.EmbedBatch(context.Background(), texts)
		if err != nil || vecs != nil {
			t.Errorf("EmbedBatch(%v) = (%v, %v), want (nil, nil)", texts, vecs, err)
		}
	}
	if got := len(api.snapshot()); got != 0 {
		t.Errorf("server saw %d requests, want 0", got)
	}
}

func TestEmbedBatchSplitsByBatchSizeAndKeepsOrder(t *testing.T) {
	api, c := newFakeAPI(t, func(w http.ResponseWriter, _ int, req embedRequest) {
		w.Header().Set("Content-Type", "application/json")
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := len(req.Input) - 1; i >= 0; i-- {
			if i < len(req.Input)-1 {
				b.WriteString(",")
			}
			b.WriteString(`{"index":` + strconv.Itoa(i) + `,"embedding":[` + req.Input[i] + `.5]}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	})
	c.batchSize = 2

	texts := []string{"0", "1", "2", "3", "4"}
	vecs, err := c.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 5 {
		t.Fatalf("got %d vectors, want 5", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 1 || int(v[0]) != i {
			t.Errorf("vecs[%d] = %v, want one component equal to %d", i, v, i)
		}
	}

	calls := api.snapshot()
	if len(calls) != 3 {
		t.Fatalf("server saw %d requests, want 3 (sizes 2+2+1)", len(calls))
	}
	for i, wantSizes := range []int{2, 2, 1} {
		if got := len(calls[i].body.Input); got != wantSizes {
			t.Errorf("request %d has %d inputs, want %d", i, got, wantSizes)
		}
	}
	for i := range texts {
		batch, pos := i/2, i%2
		if pos < len(calls[batch].body.Input) && calls[batch].body.Input[pos] != texts[i] {
			t.Errorf("request %d input %d = %q, want %q", batch, pos, calls[batch].body.Input[pos], texts[i])
		}
	}
	if calls[0].body.Model != "test-model" {
		t.Errorf("request model = %q, want test-model", calls[0].body.Model)
	}
}

func TestEmbedRequestShape(t *testing.T) {
	api, c := newFakeAPI(t, okEmbedding("1,2"))
	_, err := c.embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	calls := api.snapshot()
	if len(calls) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(calls))
	}
	if calls[0].path != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", calls[0].path)
	}
	if calls[0].body.Model != "test-model" || len(calls[0].body.Input) != 1 || calls[0].body.Input[0] != "hello" {
		t.Errorf("request body = %+v, want model test-model input [hello]", calls[0].body)
	}
	if calls[0].auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", calls[0].auth)
	}
}

func TestEmbedOmitsAuthorizationWithoutKey(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.calls = append(api.calls, recordedRequest{auth: r.Header.Get("Authorization")})
		api.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "m", 8, 5)
	if _, err := c.embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	calls := api.snapshot()
	if len(calls) != 1 || calls[0].auth != "" {
		t.Errorf("recorded Authorization = %v, want one empty value", calls)
	}
}

func TestEmbedErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler handlerFunc
		want    string
	}{
		{
			name: "non-200 status",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("upstream exploded"))
			},
			want: "API 502: upstream exploded",
		},
		{
			name: "api error field",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) {
				_, _ = w.Write([]byte(`{"error":{"message":"bad model","type":"invalid_request_error"}}`))
			},
			want: "API error [invalid_request_error]: bad model",
		},
		{
			name:    "empty data",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) { _, _ = w.Write([]byte(`{"data":[]}`)) },
			want:    "empty data in response",
		},
		{
			name: "missing index",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) {
				_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
			},
			want: "missing embedding for index 1",
		},
		{
			name: "index out of range ignored",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) {
				_, _ = w.Write([]byte(`{"data":[{"index":7,"embedding":[1]}]}`))
			},
			want: "missing embedding for index 0",
		},
		{
			name:    "invalid json",
			handler: func(w http.ResponseWriter, _ int, _ embedRequest) { _, _ = w.Write([]byte("not json")) },
			want:    "parsing response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, c := newFakeAPI(t, tc.handler)
			_, err := c.embed(context.Background(), []string{"a", "b"})
			if err == nil {
				t.Fatalf("embed returned nil error, want one containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestEmbedStatusBodyTruncated(t *testing.T) {
	long := strings.Repeat("x", 300)
	_, c := newFakeAPI(t, func(w http.ResponseWriter, _ int, _ embedRequest) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(long))
	})
	_, err := c.embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("embed returned nil error for 500 response")
	}
	if !strings.Contains(err.Error(), "API 500: ") || !strings.HasSuffix(err.Error(), "…") {
		t.Errorf("error = %v, want a truncated body ending in an ellipsis", err)
	}
	if len(err.Error()) > 220 {
		t.Errorf("error length %d, want the body truncated to 200 chars", len(err.Error()))
	}
}

func TestEmbedTransportError(t *testing.T) {
	api, _ := newFakeAPI(t, okEmbedding("1"))
	c2 := New("http://127.0.0.2:59998/v1", "", "m", 8, 1)
	_, err := c2.embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("embed against unreachable address returned nil error")
	}
	if !strings.Contains(err.Error(), "HTTP:") {
		t.Errorf("error = %v, want it to mention HTTP:", err)
	}
	if got := len(api.snapshot()); got != 0 {
		t.Errorf("fake server saw %d requests, want 0", got)
	}
}

func TestEmbedBatchCanceledContextFailsFast(t *testing.T) {
	api, c := newFakeAPI(t, okEmbedding("1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.EmbedBatch(ctx, []string{"a", "b"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to be context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "embedding batch [0:2]") {
		t.Errorf("error = %v, want it to name the failing batch range", err)
	}
	if got := len(api.snapshot()); got != 0 {
		t.Errorf("server saw %d requests, want 0 for a canceled context", got)
	}
}

func TestEmbedWithRetrySucceedsAfterFirstFailure(t *testing.T) {
	_, c := newFakeAPI(t, func(w http.ResponseWriter, call int, _ embedRequest) {
		if call == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[3]}]}`))
	})
	vecs, err := c.embedWithRetry(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("embedWithRetry: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 1 || vecs[0][0] != 3 {
		t.Errorf("vecs = %v, want [[3]]", vecs)
	}
}

func TestDimensions(t *testing.T) {
	_, c := newFakeAPI(t, okEmbedding("0.1,0.2,0.3"))
	n, err := c.Dimensions(context.Background())
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	if n != 3 {
		t.Errorf("Dimensions = %d, want 3", n)
	}
}

func TestDimensionsEmptyEmbedding(t *testing.T) {
	_, c := newFakeAPI(t, okEmbedding(""))
	_, err := c.Dimensions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty embedding in probe response") {
		t.Errorf("error = %v, want it to mention an empty probe embedding", err)
	}
}

func TestDimensionsWrapsAPIError(t *testing.T) {
	_, c := newFakeAPI(t, func(w http.ResponseWriter, _ int, _ embedRequest) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.Dimensions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "probe embedding: API 404") {
		t.Errorf("error = %v, want it to mention probe embedding and API 404", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("truncate(%q, 5) = %q, want abc", "abc", got)
	}
	if got := truncate("abcdefg", 3); got != "abc…" {
		t.Errorf("truncate(%q, 3) = %q, want abc with ellipsis", "abcdefg", got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Errorf("truncate at exact length = %q, want abc", got)
	}
}

func TestMin(t *testing.T) {
	if min(2, 1) != 1 || min(1, 2) != 1 || min(3, 3) != 3 {
		t.Error("min returned a wrong value")
	}
}
