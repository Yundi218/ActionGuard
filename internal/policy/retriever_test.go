package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	policyassets "github.com/Yundi218/ActionGuard/policies"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetrieverCandidateSQLFiltersBeforeLimit(t *testing.T) {
	ctx, pool := newRetrieverTestPool(t, "candidate_filters")
	at := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	query := Query{
		Text:            "needle",
		At:              at,
		Region:          "CN",
		ProductCategory: "electronics",
		RiskLevels:      []toolkit.Risk{toolkit.Write},
	}

	queryVector := unitVector(0)
	insertRetrievalFixture(t, pool, ctx, retrievalFixture{
		chunkID:  "z-valid",
		text:     "needle",
		from:     at.Add(-time.Hour),
		to:       at.Add(time.Hour),
		region:   "CN",
		category: "electronics",
		risk:     toolkit.Write,
		vector:   unitVector(1),
	})
	for index := 0; index < candidateLimit; index++ {
		insertRetrievalFixture(t, pool, ctx, retrievalFixture{
			chunkID:  fmt.Sprintf("a-wrong-%02d", index),
			text:     strings.Repeat("needle ", 20),
			from:     at.Add(-time.Hour),
			to:       at.Add(time.Hour),
			region:   "US",
			category: "electronics",
			risk:     toolkit.Write,
			vector:   queryVector,
		})
	}

	retriever := NewRetriever(pool, DeterministicEmbedder{})
	lexical, err := retriever.searchLexicalCandidates(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if got := evidenceIDs(lexical); !slices.Equal(got, []string{"z-valid"}) {
		t.Fatalf("lexical candidates = %v, want valid row after filtering before LIMIT", got)
	}

	vector, err := retriever.searchVectorCandidates(ctx, query, queryVector)
	if err != nil {
		t.Fatal(err)
	}
	if got := evidenceIDs(vector); !slices.Equal(got, []string{"z-valid"}) {
		t.Fatalf("vector candidates = %v, want valid row after filtering before LIMIT", got)
	}
}

func TestRetrieverUsesHalfOpenApplicabilityAndMetadataFilters(t *testing.T) {
	ctx, pool := newRetrieverTestPool(t, "metadata_filters")
	at := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	fixtures := []retrievalFixture{
		{chunkID: "lower-boundary", text: "replacement", from: at, to: at.Add(time.Hour), region: "CN", category: "electronics", risk: toolkit.Write},
		{chunkID: "upper-boundary", text: "replacement", from: at.Add(-time.Hour), to: at, region: "CN", category: "electronics", risk: toolkit.Write},
		{chunkID: "wrong-region", text: "replacement", from: at.Add(-time.Hour), to: at.Add(time.Hour), region: "US", category: "electronics", risk: toolkit.Write},
		{chunkID: "wrong-category", text: "replacement", from: at.Add(-time.Hour), to: at.Add(time.Hour), region: "CN", category: "furniture", risk: toolkit.Write},
		{chunkID: "wrong-risk", text: "replacement", from: at.Add(-time.Hour), to: at.Add(time.Hour), region: "CN", category: "electronics", risk: toolkit.HighRiskWrite},
	}
	for _, fixture := range fixtures {
		fixture.vector = mustEmbedOne(t, "replacement")
		insertRetrievalFixture(t, pool, ctx, fixture)
	}

	retriever := NewRetriever(pool, DeterministicEmbedder{})
	got, err := retriever.Search(ctx, Query{
		Text:            "replacement",
		At:              at,
		Region:          "CN",
		ProductCategory: "electronics",
		RiskLevels:      []toolkit.Risk{toolkit.Write},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := evidenceIDs(got); !slices.Equal(ids, []string{"lower-boundary"}) {
		t.Fatalf("filtered evidence = %v, want only lower-boundary", ids)
	}

	allRisks, err := retriever.Search(ctx, Query{
		Text:            "replacement",
		At:              at,
		Region:          "CN",
		ProductCategory: "electronics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := evidenceIDs(allRisks); !slices.Equal(ids, []string{"lower-boundary", "wrong-risk"}) {
		t.Fatalf("all-risk evidence = %v, want both applicable risk levels", ids)
	}
}

func TestRetrieverReturnsExactTypedEvidenceAndSourceOffsets(t *testing.T) {
	ctx, pool := newRetrieverTestPool(t, "typed_evidence")
	importer := NewImporter(DeterministicEmbedder{}, NewPostgresStore(pool))
	wantChunks := make(map[string]Chunk)
	wantBodies := make(map[string]string)
	for _, asset := range policyassets.All() {
		if err := importer.Import(ctx, asset.Name, asset.Markdown); err != nil {
			t.Fatalf("import %s: %v", asset.Name, err)
		}
		document, err := ParseMarkdown(asset.Markdown)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range document.Chunks {
			wantChunks[chunk.ID] = chunk
			wantBodies[chunk.ID] = document.Body
		}
	}

	retriever := NewRetriever(pool, DeterministicEmbedder{})
	got, err := retriever.Search(ctx, Query{
		Text:            "My headphones arrived damaged. Can I get a replacement and a coupon?",
		At:              time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		Region:          "CN",
		ProductCategory: "electronics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxResults {
		t.Fatalf("evidence count = %d, want top %d", len(got), maxResults)
	}
	sections := make(map[string]bool)
	for _, evidence := range got {
		chunk, ok := wantChunks[evidence.ChunkID]
		if !ok {
			t.Fatalf("unexpected chunk ID %q", evidence.ChunkID)
		}
		wantCitation := fmt.Sprintf("%s:%s:%s:%d-%d", evidence.PolicyID, evidence.Version, evidence.Section, evidence.StartOffset, evidence.EndOffset)
		if evidence.CitationID != wantCitation {
			t.Fatalf("citation ID = %q, want %q", evidence.CitationID, wantCitation)
		}
		if evidence.Text == "" || evidence.Region == "" || evidence.ProductCategory == "" {
			t.Fatalf("evidence has an empty required string: %#v", evidence)
		}
		if evidence.Text != chunk.Text || evidence.StartOffset != chunk.StartOffset || evidence.EndOffset != chunk.EndOffset ||
			wantBodies[evidence.ChunkID][evidence.StartOffset:evidence.EndOffset] != evidence.Text {
			t.Fatalf("evidence does not preserve source chunk: got %#v want %#v", evidence, chunk)
		}
		if evidence.PolicyID != chunk.Metadata.PolicyID || evidence.Version != chunk.Metadata.Version ||
			evidence.Section != chunk.Section || evidence.Region != chunk.Metadata.Region ||
			evidence.ProductCategory != chunk.Metadata.ProductCategory || evidence.RiskLevel != chunk.Metadata.RiskLevel ||
			!evidence.EffectiveFrom.Equal(chunk.Metadata.EffectiveFrom) || !evidence.EffectiveTo.Equal(chunk.Metadata.EffectiveTo) {
			t.Fatalf("typed evidence metadata differs from source chunk: got %#v want %#v", evidence, chunk.Metadata)
		}
		if !equalOptionalInt64(evidence.MaxCouponCents, chunk.Metadata.MaxCouponCents) {
			t.Fatalf("max coupon cents = %v, want %v", evidence.MaxCouponCents, chunk.Metadata.MaxCouponCents)
		}
		sections[evidence.PolicyID+":"+evidence.Section] = true
	}
	for _, expected := range []string{"damaged_goods:Replacement eligibility", "customer_care:Coupon compensation"} {
		if !sections[expected] {
			t.Fatalf("damaged-headphones top five missing %q: %v", expected, sections)
		}
	}
}

func TestRetrieverRRFMergesDuplicatesAndUsesStableChunkIDTies(t *testing.T) {
	t.Run("fusion", func(t *testing.T) {
		got := fuseRankings(
			[]Evidence{{ChunkID: "b"}, {ChunkID: "a"}},
			[]Evidence{{ChunkID: "a"}, {ChunkID: "c"}},
			maxResults,
		)
		if ids := evidenceIDs(got); !slices.Equal(ids, []string{"a", "b", "c"}) {
			t.Fatalf("fused IDs = %v, want [a b c]", ids)
		}
		wantScores := map[string]float64{
			"a": 1.0/float64(rrfK+2) + 1.0/float64(rrfK+1),
			"b": 1.0 / float64(rrfK+1),
			"c": 1.0 / float64(rrfK+2),
		}
		for _, evidence := range got {
			if math.Abs(evidence.Score-wantScores[evidence.ChunkID]) > 1e-15 {
				t.Fatalf("score for %s = %.17f, want %.17f", evidence.ChunkID, evidence.Score, wantScores[evidence.ChunkID])
			}
		}
	})

	t.Run("stable tie", func(t *testing.T) {
		got := fuseRankings([]Evidence{{ChunkID: "b"}}, []Evidence{{ChunkID: "a"}}, maxResults)
		if ids := evidenceIDs(got); !slices.Equal(ids, []string{"a", "b"}) {
			t.Fatalf("tied IDs = %v, want stable chunk ID order [a b]", ids)
		}
	})
}

func TestRetrieverLimitSemantics(t *testing.T) {
	ctx, pool := newRetrieverTestPool(t, "limits")
	at := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 7; index++ {
		text := fmt.Sprintf("replacement policy %d", index)
		insertRetrievalFixture(t, pool, ctx, retrievalFixture{
			chunkID:  fmt.Sprintf("chunk-%d", index),
			text:     text,
			from:     at.Add(-time.Hour),
			to:       at.Add(time.Hour),
			region:   "CN",
			category: "electronics",
			risk:     toolkit.Write,
			vector:   mustEmbedOne(t, text),
		})
	}
	retriever := NewRetriever(pool, DeterministicEmbedder{})
	base := Query{Text: "replacement", At: at, Region: "CN", ProductCategory: "electronics"}

	for _, test := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "zero defaults", limit: 0, want: maxResults},
		{name: "above max clamps", limit: 99, want: maxResults},
		{name: "positive respected", limit: 2, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := base
			query.Limit = test.limit
			got, err := retriever.Search(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.want {
				t.Fatalf("result count = %d, want %d", len(got), test.want)
			}
		})
	}

	base.Limit = -1
	if _, err := retriever.Search(ctx, base); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("negative limit error = %v, want ErrInvalidQuery", err)
	}
}

func TestRetrieverRejectsMissingRequiredQueryFields(t *testing.T) {
	valid := Query{
		Text:            "replacement",
		At:              time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		Region:          "CN",
		ProductCategory: "electronics",
	}
	tests := map[string]func(*Query){
		"text":             func(query *Query) { query.Text = " \t" },
		"region":           func(query *Query) { query.Region = "" },
		"product category": func(query *Query) { query.ProductCategory = " " },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			query := valid
			mutate(&query)
			if _, err := NewRetriever(nil, DeterministicEmbedder{}).Search(context.Background(), query); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestRetrieverUsesParameterizedQueryInputs(t *testing.T) {
	ctx, pool := newRetrieverTestPool(t, "parameters")
	at := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	insertRetrievalFixture(t, pool, ctx, retrievalFixture{
		chunkID:  "safe-row",
		text:     "replacement",
		from:     at.Add(-time.Hour),
		to:       at.Add(time.Hour),
		region:   "CN",
		category: "electronics",
		risk:     toolkit.Write,
		vector:   mustEmbedOne(t, "replacement"),
	})
	retriever := NewRetriever(pool, DeterministicEmbedder{})

	queries := []Query{
		{Text: "replacement'); drop table policy_documents; --", At: at, Region: "CN", ProductCategory: "electronics"},
		{Text: "replacement", At: at, Region: "CN' OR '1'='1", ProductCategory: "electronics"},
		{Text: "replacement", At: at, Region: "CN", ProductCategory: "electronics' OR '1'='1"},
		{Text: "replacement", At: at, Region: "CN", ProductCategory: "electronics", RiskLevels: []toolkit.Risk{"write') OR true --"}},
	}
	for index, query := range queries {
		if _, err := retriever.Search(ctx, query); err != nil {
			t.Fatalf("query %d returned error: %v", index, err)
		}
	}
	var tableExists bool
	if err := pool.QueryRow(ctx, `select to_regclass('policy_documents') is not null`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists {
		t.Fatal("parameter-like query input changed the database schema")
	}
}

func TestRetrieverSanitizesEmbeddingAndDatabaseErrors(t *testing.T) {
	query := Query{
		Text:            "replacement",
		At:              time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		Region:          "CN",
		ProductCategory: "electronics",
	}
	secret := "postgres://secret-user:secret-password@private-db/policies"
	if _, err := NewRetriever(nil, failingRetrieverEmbedder{err: errors.New(secret)}).Search(context.Background(), query); !errors.Is(err, ErrEmbedding) || strings.Contains(err.Error(), secret) {
		t.Fatalf("embedding error = %v, want sanitized ErrEmbedding", err)
	}
	if _, err := NewRetriever(nil, DeterministicEmbedder{}).Search(context.Background(), query); !errors.Is(err, ErrRetrieval) || strings.Contains(err.Error(), secret) {
		t.Fatalf("database error = %v, want sanitized ErrRetrieval", err)
	}
}

type failingRetrieverEmbedder struct {
	err error
}

func (embedder failingRetrieverEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, embedder.err
}

type retrievalFixture struct {
	chunkID  string
	text     string
	from     time.Time
	to       time.Time
	region   string
	category string
	risk     toolkit.Risk
	vector   []float32
}

func insertRetrievalFixture(t *testing.T, pool *pgxpool.Pool, ctx context.Context, fixture retrievalFixture) {
	t.Helper()
	if fixture.vector == nil {
		fixture.vector = mustEmbedOne(t, fixture.text)
	}
	policyID := "policy-" + fixture.chunkID
	version := "v1"
	if _, err := pool.Exec(ctx, `
		insert into policy_documents (
			policy_id, version, source_name, effective_from, effective_to, region,
			product_category, risk_level, max_coupon_cents, content_sha256
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, policyID, version, fixture.chunkID+".md", fixture.from, fixture.to, fixture.region,
		fixture.category, fixture.risk, int64(2000), strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert document %s: %v", fixture.chunkID, err)
	}
	if _, err := pool.Exec(ctx, `
		insert into policy_chunks (
			id, policy_id, version, section, content, start_offset, end_offset, embedding
		) values ($1, $2, $3, $4, $5, $6, $7, $8)
	`, fixture.chunkID, policyID, version, "Rules", fixture.text, 0, len(fixture.text), vectorLiteral(fixture.vector)); err != nil {
		t.Fatalf("insert chunk %s: %v", fixture.chunkID, err)
	}
}

func newRetrieverTestPool(t *testing.T, prefix string) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminPool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	schema := fmt.Sprintf("retriever_%s_%d", prefix, time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	migrationURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	parameters := migrationURL.Query()
	parameters.Set("search_path", schema+",public")
	migrationURL.RawQuery = parameters.Encode()
	pool, err := database.Open(ctx, migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func mustEmbedOne(t *testing.T, text string) []float32 {
	t.Helper()
	vectors, err := (DeterministicEmbedder{}).Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatal(err)
	}
	return vectors[0]
}

func unitVector(dimension int) []float32 {
	vector := make([]float32, EmbeddingDimensions)
	vector[dimension] = 1
	return vector
}

func evidenceIDs(evidence []Evidence) []string {
	ids := make([]string, len(evidence))
	for index := range evidence {
		ids[index] = evidence[index].ChunkID
	}
	return ids
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
