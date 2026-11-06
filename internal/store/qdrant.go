package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// Client is a Qdrant gRPC client scoped to one collection.
type Client struct {
	baseURL    string
	apiKey     string
	collection string
	client     *qdrant.Client
}

// New creates a Qdrant client.
func New(baseURL, apiKey, collection string) *Client {
	host, port := parseHostPort(baseURL)

	cfg := &qdrant.Config{
		Host: host,
		Port: port,
	}
	if apiKey != "" {
		cfg.APIKey = apiKey
	}

	qClient, err := qdrant.NewClient(cfg)
	if err != nil {
		panic(fmt.Sprintf("failed to create Qdrant client: %v", err))
	}

	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		collection: collection,
		client:     qClient,
	}
}

func parseHostPort(url string) (string, int) {
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "https://")

	parts := strings.Split(url, ":")
	if len(parts) == 2 {
		var port int
		if n, err := fmt.Sscanf(parts[1], "%d", &port); err != nil || n != 1 {
			return parts[0], 6333
		}
		return parts[0], port
	}
	return url, 6333
}

// Point is a single vector entry.
type Point struct {
	ID      string
	Vector  []float32
	Payload map[string]any
}

func (c *Client) EnsureCollection(ctx context.Context, vectorSize int, distance string) error {
	exists, err := c.client.CollectionExists(ctx, c.collection)
	if err != nil {
		return fmt.Errorf("checking collection: %w", err)
	}
	if exists {
		return nil
	}

	qdrantDistance := qdrant.Distance_Cosine
	switch strings.ToUpper(distance) {
	case "COSINE":
		qdrantDistance = qdrant.Distance_Cosine
	case "DOT":
		qdrantDistance = qdrant.Distance_Dot
	case "EUCLID":
		qdrantDistance = qdrant.Distance_Euclid
	}

	vectorParams := &qdrant.VectorParams{
		Size:     uint64(vectorSize),
		Distance: qdrantDistance,
	}

	vectorsConfig := qdrant.NewVectorsConfig(vectorParams)

	optConfig := &qdrant.OptimizersConfigDiff{
		IndexingThreshold: qdrant.PtrOf(uint64(20_000)),
		MemmapThreshold:   qdrant.PtrOf(uint64(50_000)),
	}

	hnswConfig := &qdrant.HnswConfigDiff{
		M:                 qdrant.PtrOf(uint64(16)),
		EfConstruct:       qdrant.PtrOf(uint64(100)),
		FullScanThreshold: qdrant.PtrOf(uint64(10_000)),
		OnDisk:            qdrant.PtrOf(false),
	}

	createReq := &qdrant.CreateCollection{
		CollectionName:   c.collection,
		VectorsConfig:    vectorsConfig,
		OptimizersConfig: optConfig,
		HnswConfig:       hnswConfig,
	}

	if err := c.client.CreateCollection(ctx, createReq); err != nil {
		return fmt.Errorf("creating collection: %w", err)
	}

	payloadFields := map[string]*qdrant.PayloadIndexParams{
		"source":  qdrant.NewPayloadIndexParamsKeyword(nil),
		"context": qdrant.NewPayloadIndexParamsKeyword(nil),
		"tags":    qdrant.NewPayloadIndexParamsKeyword(nil),
		"title":   qdrant.NewPayloadIndexParamsText(nil),
	}

	for field, params := range payloadFields {
		req := &qdrant.CreateFieldIndexCollection{
			CollectionName:   c.collection,
			FieldName:        field,
			FieldIndexParams: params,
		}
		if _, err := c.client.CreateFieldIndex(ctx, req); err != nil {
			return fmt.Errorf("creating index for %q: %w", field, err)
		}
	}

	return nil
}

func (c *Client) UpsertPoints(ctx context.Context, points []Point) error {
	const batchSize = 100
	for start := 0; start < len(points); start += batchSize {
		end := start + batchSize
		if end > len(points) {
			end = len(points)
		}
		if err := c.upsertBatch(ctx, points[start:end]); err != nil {
			return fmt.Errorf("upsert batch [%d:%d]: %w", start, end, err)
		}
	}
	return nil
}

func (c *Client) upsertBatch(ctx context.Context, points []Point) error {
	qp := make([]*qdrant.PointStruct, len(points))
	for i, p := range points {
		payload := make(map[string]*qdrant.Value, len(p.Payload))
		for k, v := range p.Payload {
			qv, err := qdrant.NewValue(v)
			if err != nil {
				return fmt.Errorf("converting payload value for %q: %w", k, err)
			}
			payload[k] = qv
		}

		qid := qdrant.NewIDUUID(p.ID)

		vectors := qdrant.NewVectorsDense(p.Vector)

		qp[i] = &qdrant.PointStruct{
			Id:      qid,
			Payload: payload,
			Vectors: vectors,
		}
	}

	req := &qdrant.UpsertPoints{
		CollectionName: c.collection,
		Points:         qp,
	}

	if _, err := c.client.Upsert(ctx, req); err != nil {
		return err
	}
	return nil
}

func (c *Client) DeleteBySource(ctx context.Context, sourcePath string) error {
	filter := &qdrant.Filter{
		Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword("source", sourcePath),
		},
	}

	req := &qdrant.DeletePoints{
		CollectionName: c.collection,
		Points:         qdrant.NewPointsSelectorFilter(filter),
	}

	if _, err := c.client.Delete(ctx, req); err != nil {
		return err
	}
	return nil
}

type SourceIndex map[string]string

func (c *Client) ScrollSourceIndex(ctx context.Context) (SourceIndex, error) {
	index := make(SourceIndex)
	var offset *qdrant.PointId

	limit := uint32(1000)

	for {
		req := &qdrant.ScrollPoints{
			CollectionName: c.collection,
			Limit:          &limit,
			WithPayload:    qdrant.NewWithPayloadInclude("source", "file_hash"),
			WithVectors:    qdrant.NewWithVectorsEnable(false),
		}

		if offset != nil {
			req.Offset = offset
		}

		points, nextOffset, err := c.client.ScrollAndOffset(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("scroll: %w", err)
		}

		for _, pt := range points {
			payload := pt.Payload
			if payload == nil {
				continue
			}

			source := ""
			fileHash := ""

			if sourceVal, ok := payload["source"]; ok && sourceVal != nil {
				source = sourceVal.GetStringValue()
			}

			if fileHashVal, ok := payload["file_hash"]; ok && fileHashVal != nil {
				fileHash = fileHashVal.GetStringValue()
			}

			if source != "" {
				index[source] = fileHash
			}
		}

		if nextOffset == nil {
			break
		}
		offset = nextOffset
	}

	return index, nil
}

func PointID(filePath string, chunkIdx int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", filePath, chunkIdx)))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
