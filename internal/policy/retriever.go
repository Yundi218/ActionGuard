package policy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	candidateLimit = 20
	maxResults     = 5
	rrfK           = 60
)

var (
	ErrInvalidQuery = errors.New("invalid policy query")
	ErrRetrieval    = errors.New("policy retrieval failed")
)

type Query struct {
	Text            string
	At              time.Time
	Region          string
	ProductCategory string
	RiskLevels      []toolkit.Risk
	Limit           int
}

type Evidence struct {
	ChunkID         string       `json:"chunk_id"`
	CitationID      string       `json:"citation_id"`
	PolicyID        string       `json:"policy_id"`
	Version         string       `json:"version"`
	Section         string       `json:"section"`
	StartOffset     int          `json:"start_offset"`
	EndOffset       int          `json:"end_offset"`
	EffectiveFrom   time.Time    `json:"effective_from"`
	EffectiveTo     time.Time    `json:"effective_to"`
	Region          string       `json:"region"`
	ProductCategory string       `json:"product_category"`
	RiskLevel       toolkit.Risk `json:"risk_level"`
	MaxCouponCents  *int64       `json:"max_coupon_cents,omitempty"`
	Text            string       `json:"text"`
	Score           float64      `json:"score"`
}

type Retriever struct {
	store    *PostgresStore
	embedder Embedder
}

func NewRetriever(pool *pgxpool.Pool, embedder Embedder) *Retriever {
	return &Retriever{
		store:    NewPostgresStore(pool),
		embedder: embedder,
	}
}

func (retriever *Retriever) Search(ctx context.Context, query Query) ([]Evidence, error) {
	limit, err := validateQuery(query)
	if err != nil {
		return nil, err
	}
	if retriever == nil || retriever.embedder == nil {
		return nil, ErrRetrieval
	}

	vectors, err := retriever.embedder.Embed(ctx, []string{query.Text})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrEmbedding
	}
	if len(vectors) != 1 || len(vectors[0]) != EmbeddingDimensions || !finiteVector(vectors[0]) {
		return nil, ErrEmbedding
	}

	lexical, err := retriever.searchLexicalCandidates(ctx, query)
	if err != nil {
		return nil, err
	}
	vector, err := retriever.searchVectorCandidates(ctx, query, vectors[0])
	if err != nil {
		return nil, err
	}
	return fuseRankings(lexical, vector, limit), nil
}

func (retriever *Retriever) searchLexicalCandidates(ctx context.Context, query Query) ([]Evidence, error) {
	if retriever == nil || retriever.store == nil {
		return nil, ErrRetrieval
	}
	return retriever.store.searchLexicalCandidates(ctx, query)
}

func (retriever *Retriever) searchVectorCandidates(ctx context.Context, query Query, vector []float32) ([]Evidence, error) {
	if retriever == nil || retriever.store == nil {
		return nil, ErrRetrieval
	}
	return retriever.store.searchVectorCandidates(ctx, query, vector)
}

func validateQuery(query Query) (int, error) {
	if strings.TrimSpace(query.Text) == "" || strings.TrimSpace(query.Region) == "" || strings.TrimSpace(query.ProductCategory) == "" || query.Limit < 0 {
		return 0, ErrInvalidQuery
	}
	if query.Limit == 0 || query.Limit > maxResults {
		return maxResults, nil
	}
	return query.Limit, nil
}

func fuseRankings(lexical, vector []Evidence, limit int) []Evidence {
	fused := make(map[string]Evidence, len(lexical)+len(vector))
	addRanking := func(ranking []Evidence) {
		for index, evidence := range ranking {
			stored, exists := fused[evidence.ChunkID]
			if !exists {
				stored = evidence
			}
			stored.Score += 1 / float64(rrfK+index+1)
			fused[evidence.ChunkID] = stored
		}
	}
	addRanking(lexical)
	addRanking(vector)

	result := make([]Evidence, 0, len(fused))
	for _, evidence := range fused {
		result = append(result, evidence)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score == result[right].Score {
			return result[left].ChunkID < result[right].ChunkID
		}
		return result[left].Score > result[right].Score
	})
	if limit < len(result) {
		result = result[:limit]
	}
	return result
}

func citationID(evidence Evidence) string {
	return fmt.Sprintf("%s:%s:%s:%d-%d", evidence.PolicyID, evidence.Version, evidence.Section, evidence.StartOffset, evidence.EndOffset)
}
