package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func newDeadClient(t *testing.T) *Client {
	t.Helper()
	return New("http://127.0.0.1:19999", "", "test-collection")
}

func TestParseHostPort(t *testing.T) {
	tests := []struct {
		url      string
		wantHost string
		wantPort int
	}{
		{"http://localhost:6333", "localhost", 6333},
		{"https://q.example:7000", "q.example", 7000},
		{"localhost:1234", "localhost", 1234},
		{"http://localhost", "localhost", 6333},
		{"localhost", "localhost", 6333},
		{"https://q.example:6400/grpc", "q.example", 6400},
		{"http://127.0.0.1:19999", "127.0.0.1", 19999},
	}
	for _, tc := range tests {
		host, port := parseHostPort(tc.url)
		if host != tc.wantHost || port != tc.wantPort {
			t.Errorf("parseHostPort(%q) = (%q, %d), want (%q, %d)", tc.url, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestPointIDIsStableUUIDv5Shaped(t *testing.T) {
	id := PointID("/vault/note.md", 0)
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(id) {
		t.Errorf("PointID = %q, want a UUID with version 5 and RFC 4122 variant", id)
	}
	if again := PointID("/vault/note.md", 0); again != id {
		t.Errorf("PointID is not deterministic: %q then %q", id, again)
	}
	if other := PointID("/vault/note.md", 1); other == id {
		t.Errorf("PointID with a different chunk index equals %q, want a distinct id", id)
	}
	if other := PointID("/vault/other.md", 0); other == id {
		t.Errorf("PointID with a different path equals %q, want a distinct id", id)
	}
}

func TestNewStoresFields(t *testing.T) {
	c := New("http://127.0.0.1:19999/", "key-abc", "coll")
	if c.baseURL != "http://127.0.0.1:19999" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
	if c.apiKey != "key-abc" || c.collection != "coll" {
		t.Errorf("client fields = %+v, want key key-abc collection coll", c)
	}
}

func TestEnsureCollectionCanceled(t *testing.T) {
	c := newDeadClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.EnsureCollection(ctx, 4, "Cosine")
	if err == nil {
		t.Fatal("EnsureCollection with canceled context returned nil error")
	}
	if !strings.Contains(err.Error(), "checking collection") {
		t.Errorf("error = %v, want it to mention checking collection", err)
	}
}

func TestUpsertPointsEmptyMakesNoRequest(t *testing.T) {
	c := newDeadClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.UpsertPoints(ctx, nil); err != nil {
		t.Errorf("UpsertPoints with no points = %v, want nil", err)
	}
	if err := c.UpsertPoints(ctx, []Point{}); err != nil {
		t.Errorf("UpsertPoints with empty slice = %v, want nil", err)
	}
}

func TestUpsertPointsCanceledReportsBatchRange(t *testing.T) {
	c := newDeadClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	points := make([]Point, 150)
	for i := range points {
		points[i] = Point{ID: PointID("x", i), Vector: []float32{1}, Payload: map[string]any{"source": "x"}}
	}
	err := c.UpsertPoints(ctx, points)
	if err == nil {
		t.Fatal("UpsertPoints with canceled context returned nil error")
	}
	if !strings.HasPrefix(err.Error(), "upsert batch [0:100]: ") {
		t.Errorf("error = %v, want it to report the failing batch range [0:100]", err)
	}
}

func TestUpsertPointsRejectsUnconvertiblePayload(t *testing.T) {
	c := newDeadClient(t)
	err := c.UpsertPoints(context.Background(), []Point{
		{ID: PointID("x", 0), Vector: []float32{1}, Payload: map[string]any{"bad": make(chan int)}},
	})
	if err == nil {
		t.Fatal("UpsertPoints accepted an unconvertible payload value")
	}
	if !strings.Contains(err.Error(), `converting payload value for "bad"`) {
		t.Errorf("error = %v, want it to name the payload key that failed conversion", err)
	}
}

func TestDeleteBySourceCanceled(t *testing.T) {
	c := newDeadClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.DeleteBySource(ctx, "/vault/note.md")
	if err == nil {
		t.Fatal("DeleteBySource with canceled context returned nil error")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v, want a canceled RPC error", err)
	}
}

func TestScrollSourceIndexCanceled(t *testing.T) {
	c := newDeadClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	index, err := c.ScrollSourceIndex(ctx)
	if err == nil {
		t.Fatal("ScrollSourceIndex with canceled context returned nil error")
	}
	if index != nil {
		t.Errorf("index = %v, want nil alongside the error", index)
	}
	if !strings.Contains(err.Error(), "scroll:") {
		t.Errorf("error = %v, want it to mention scroll", err)
	}
}
