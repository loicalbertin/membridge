package search

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// Client is a Qdrant search client scoped to one collection.
type Client struct {
	baseURL    string
	apiKey     string
	collection string
	client     *qdrant.Client
}

// New creates a search client.
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

func (c *Client) ensurePayloadIndexes(ctx context.Context) error {
	collInfo, err := c.client.GetCollectionInfo(ctx, c.collection)
	if err != nil {
		return fmt.Errorf("getting collection info: %w", err)
	}

	payloadSchema := collInfo.GetPayloadSchema()
	if payloadSchema == nil {
		payloadSchema = make(map[string]*qdrant.PayloadSchemaInfo)
	}

	requiredIndexes := map[string]*qdrant.PayloadIndexParams{
		"source":  qdrant.NewPayloadIndexParamsKeyword(&qdrant.KeywordIndexParams{EnableHnsw: qdrant.PtrOf(true)}),
		"context": qdrant.NewPayloadIndexParamsKeyword(&qdrant.KeywordIndexParams{EnableHnsw: qdrant.PtrOf(true)}),
		"tags":    qdrant.NewPayloadIndexParamsKeyword(&qdrant.KeywordIndexParams{EnableHnsw: qdrant.PtrOf(true)}),
	}

	for field, params := range requiredIndexes {
		existingParams := payloadSchema[field]
		if existingParams == nil {
			req := &qdrant.CreateFieldIndexCollection{
				CollectionName:   c.collection,
				FieldName:        field,
				FieldType:        qdrant.PtrOf(qdrant.FieldType_FieldTypeKeyword),
				FieldIndexParams: params,
			}
			if _, err := c.client.CreateFieldIndex(ctx, req); err != nil {
				return fmt.Errorf("creating index for %q: %w", field, err)
			}
		}
	}

	return nil
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

// ── search ────────────────────────────────────────────────────────────────────

// Params controls a search request.
type Params struct {
	Vector  []float32
	Context string   // exact match filter on "context" field; empty = no filter
	Tags    []string // any-match filter on "tags" field; empty = no filter
	Limit   int      // defaults to 5
}

// Result is a single matching chunk.
type Result struct {
	Title        string
	Section      string
	Content      string
	Source       string
	RelativePath string
	Tags         []string
	Context      string
	Score        float32
}

// Search performs a vector similarity search with optional metadata filters.
func (c *Client) Search(ctx context.Context, p Params) ([]Result, error) {
	if p.Limit <= 0 {
		p.Limit = 5
	}

	var filter *qdrant.Filter
	if f := buildFilter(p.Context, p.Tags); f != nil {
		filter = buildQdrantFilter(f)
	}

	query := qdrant.NewQueryNearest(qdrant.NewVectorInputDense(p.Vector))

	req := &qdrant.QueryPoints{
		CollectionName: c.collection,
		Query:          query,
		Limit:          qdrant.PtrOf(uint64(p.Limit)),
		WithPayload:    qdrant.NewWithPayloadEnable(true),
		WithVectors:    qdrant.NewWithVectorsEnable(false),
		ScoreThreshold: qdrant.PtrOf(float32(0.3)),
	}
	if filter != nil {
		req.Filter = filter
	}

	points, err := c.client.Query(ctx, req)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(points))
	for _, pt := range points {
		results = append(results, ptToResult(pt))
	}
	return results, nil
}

// ── facets ────────────────────────────────────────────────────────────────────

// FacetValue is a single value + doc count from a facet query.
type FacetValue struct {
	Value string
	Count int
}

// Facets returns the distinct values (and counts) for a given payload field.
// Uses Qdrant's /facets endpoint (v1.9+). Falls back to scroll if unavailable.
func (c *Client) Facets(ctx context.Context, field string, limit int) ([]FacetValue, error) {
	if limit <= 0 {
		limit = 200
	}

	if err := c.ensurePayloadIndexes(ctx); err != nil {
		return nil, fmt.Errorf("ensuring payload indexes: %w", err)
	}

	req := &qdrant.FacetCounts{
		CollectionName: c.collection,
		Key:            field,
		Limit:          qdrant.PtrOf(uint64(limit)),
	}
	facet, err := c.client.Facet(ctx, req)
	if err != nil {
		// Qdrant < 1.9: fall back to scroll-based aggregation
		return c.facetsByScroll(ctx, field, limit)
	}

	out := make([]FacetValue, 0, len(facet))
	for _, h := range facet {
		out = append(out, FacetValue{
			Value: h.GetValue().GetStringValue(),
			Count: int(h.GetCount()),
		})
	}
	return out, nil
}

// facetsByScroll aggregates distinct field values by paging through all points.
// Fallback for Qdrant instances that don't support /facets.
func (c *Client) facetsByScroll(ctx context.Context, field string, limit int) ([]FacetValue, error) {
	counts := make(map[string]int)
	var offset *qdrant.PointId
	limitScroll := uint32(1000)

	for {
		req := &qdrant.ScrollPoints{
			CollectionName: c.collection,
			Limit:          &limitScroll,
			WithPayload:    qdrant.NewWithPayloadInclude(field),
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
			payload := pt.GetPayload()
			if payload == nil {
				continue
			}

			if fieldVal, ok := payload[field]; ok && fieldVal != nil {
				switch v := fieldVal.GetKind().(type) {
				case *qdrant.Value_StringValue:
					counts[v.StringValue]++
				case *qdrant.Value_ListValue:
					for _, item := range v.ListValue.GetValues() {
						if s := item.GetStringValue(); s != "" {
							counts[s]++
						}
					}
				}
			}
		}

		if nextOffset == nil {
			break
		}
		offset = nextOffset
	}

	out := make([]FacetValue, 0, len(counts))
	for val, count := range counts {
		out = append(out, FacetValue{Value: val, Count: count})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ── filter builder ────────────────────────────────────────────────────────────

// buildFilter constructs a Qdrant filter from context and tags.
//
//   - context: exact keyword match in the "must" clause
//   - tags: any-of match in the "should" clause with min_should = 1
//
// Returns nil when no filter is needed.
func buildFilter(filterContext string, tags []string) map[string]any {
	var must []map[string]any
	var should []map[string]any

	if filterContext != "" {
		must = append(must, keywordMatch("context", filterContext))
	}

	for _, tag := range tags {
		if t := strings.TrimSpace(tag); t != "" {
			should = append(should, keywordMatch("tags", t))
		}
	}

	if len(must) == 0 && len(should) == 0 {
		return nil
	}

	f := make(map[string]any)
	if len(must) > 0 {
		f["must"] = must
	}
	if len(should) > 0 {
		f["should"] = should
		minShould := qdrant.PtrOf(uint32(1))
		f["minShould"] = minShould
	}
	return f
}

func keywordMatch(field, value string) map[string]any {
	return map[string]any{
		"key":   field,
		"match": map[string]any{"value": value},
	}
}

func buildQdrantFilter(filter map[string]any) *qdrant.Filter {
	var qdrantFilter qdrant.Filter

	if must, ok := filter["must"].([]map[string]any); ok && len(must) > 0 {
		for _, condition := range must {
			if c := buildQdrantCondition(condition); c != nil {
				qdrantFilter.Must = append(qdrantFilter.Must, c)
			}
		}
	}

	if should, ok := filter["should"].([]map[string]any); ok && len(should) > 0 {
		for _, condition := range should {
			if c := buildQdrantCondition(condition); c != nil {
				qdrantFilter.Should = append(qdrantFilter.Should, c)
			}
		}
		minShould := &qdrant.MinShould{MinCount: 1}
		qdrantFilter.MinShould = minShould
	}

	return &qdrantFilter
}

func buildQdrantCondition(condition map[string]any) *qdrant.Condition {
	if match, ok := condition["match"].(map[string]any); ok {
		if key, k := condition["key"].(string); k {
			if value, v := match["value"].(string); v {
				return qdrant.NewMatchKeyword(key, value)
			}
		}
	}
	return nil
}

func ptToResult(pt *qdrant.ScoredPoint) Result {
	r := Result{Score: pt.GetScore()}
	payload := pt.GetPayload()

	if payload == nil {
		return r
	}

	if titleVal, ok := payload["title"]; ok && titleVal != nil {
		r.Title = titleVal.GetStringValue()
	}
	if sectionVal, ok := payload["section"]; ok && sectionVal != nil {
		r.Section = sectionVal.GetStringValue()
	}
	if contentVal, ok := payload["content"]; ok && contentVal != nil {
		r.Content = contentVal.GetStringValue()
	}
	if sourceVal, ok := payload["source"]; ok && sourceVal != nil {
		r.Source = sourceVal.GetStringValue()
	}
	if relPathVal, ok := payload["relative_path"]; ok && relPathVal != nil {
		r.RelativePath = relPathVal.GetStringValue()
	}
	if contextVal, ok := payload["context"]; ok && contextVal != nil {
		r.Context = contextVal.GetStringValue()
	}
	if tagsVal, ok := payload["tags"]; ok && tagsVal != nil {
		if list := tagsVal.GetListValue(); list != nil {
			for _, item := range list.GetValues() {
				if s := item.GetStringValue(); s != "" {
					r.Tags = append(r.Tags, s)
				}
			}
		} else if s := tagsVal.GetStringValue(); s != "" {
			r.Tags = strings.Split(s, ",")
		}
	}
	return r
}

// ── Document builder ────────────────────────────────────────────────────────────

// DocumentResult is a full document reconstructed from its chunks.
type DocumentResult struct {
	Title        string
	RelativePath string
	Context      string
	Tags         []string
	Content      string // chunks joined in order
}

// GetDocument fetches all chunks for a relative_path and reconstructs the document.
func (c *Client) GetDocument(ctx context.Context, relativePath string) (*DocumentResult, error) {
	filter := &qdrant.Filter{
		Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword("relative_path", relativePath),
		},
	}

	limit := uint32(500) // well above any realistic chunk count
	req := &qdrant.ScrollPoints{
		CollectionName: c.collection,
		Filter:         filter,
		Limit:          &limit,
		WithPayload:    qdrant.NewWithPayloadEnable(true),
		WithVectors:    qdrant.NewWithVectorsEnable(false),
	}

	points, _, err := c.client.ScrollAndOffset(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return nil, nil
	}

	// Sort by chunk_index
	slices.SortFunc(points, func(a, b *qdrant.RetrievedPoint) int {
		ai := int(a.GetPayload()["chunk_index"].GetIntegerValue())
		bi := int(b.GetPayload()["chunk_index"].GetIntegerValue())
		return ai - bi
	})

	doc := &DocumentResult{}
	var parts []string

	for _, pt := range points {
		payload := pt.GetPayload()
		if doc.Title == "" {
			if v := payload["title"]; v != nil {
				doc.Title = v.GetStringValue()
			}
		}
		if doc.RelativePath == "" {
			if v := payload["relative_path"]; v != nil {
				doc.RelativePath = v.GetStringValue()
			}
		}
		if doc.Context == "" {
			if v := payload["context"]; v != nil {
				doc.Context = v.GetStringValue()
			}
		}
		if len(doc.Tags) == 0 {
			if v := payload["tags"]; v != nil {
				if list := v.GetListValue(); list != nil {
					for _, item := range list.GetValues() {
						if s := item.GetStringValue(); s != "" {
							doc.Tags = append(doc.Tags, s)
						}
					}
				}
			}
		}
		if v := payload["content"]; v != nil {
			parts = append(parts, v.GetStringValue())
		}
	}

	doc.Content = strings.Join(parts, "\n\n")
	return doc, nil
}
