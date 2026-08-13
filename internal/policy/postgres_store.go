package policy

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lexicalCandidatesSQL = `
	select
		chunk.id,
		document.policy_id,
		document.version,
		chunk.section,
		chunk.start_offset,
		chunk.end_offset,
		document.effective_from,
		document.effective_to,
		document.region,
		document.product_category,
		document.risk_level,
		document.max_coupon_cents,
		chunk.content
	from policy_chunks chunk
	join policy_documents document
	  on document.policy_id = chunk.policy_id and document.version = chunk.version
	where document.effective_from <= $2
	  and $2 < document.effective_to
	  and document.region = $3
	  and document.product_category = $4
	  and (cardinality($5::text[]) = 0 or document.risk_level = any($5::text[]))
	  and chunk.search_vector @@ websearch_to_tsquery('english', $1)
	order by ts_rank_cd(chunk.search_vector, websearch_to_tsquery('english', $1)) desc, chunk.id asc
	limit $6
`

const vectorCandidatesSQL = `
	select
		chunk.id,
		document.policy_id,
		document.version,
		chunk.section,
		chunk.start_offset,
		chunk.end_offset,
		document.effective_from,
		document.effective_to,
		document.region,
		document.product_category,
		document.risk_level,
		document.max_coupon_cents,
		chunk.content
	from policy_chunks chunk
	join policy_documents document
	  on document.policy_id = chunk.policy_id and document.version = chunk.version
	where document.effective_from <= $2
	  and $2 < document.effective_to
	  and document.region = $3
	  and document.product_category = $4
	  and (cardinality($5::text[]) = 0 or document.risk_level = any($5::text[]))
	order by chunk.embedding <=> $1::vector, chunk.id asc
	limit $6
`

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (store *PostgresStore) searchLexicalCandidates(ctx context.Context, query Query) ([]Evidence, error) {
	if store == nil || store.pool == nil {
		return nil, ErrRetrieval
	}
	rows, err := store.pool.Query(ctx, lexicalCandidatesSQL,
		query.Text,
		query.At,
		query.Region,
		query.ProductCategory,
		riskLevelStrings(query.RiskLevels),
		candidateLimit,
	)
	if err != nil {
		return nil, mapRetrievalError(ctx, err)
	}
	return scanEvidenceRows(ctx, rows)
}

func (store *PostgresStore) searchVectorCandidates(ctx context.Context, query Query, vector []float32) ([]Evidence, error) {
	if store == nil || store.pool == nil {
		return nil, ErrRetrieval
	}
	rows, err := store.pool.Query(ctx, vectorCandidatesSQL,
		vectorLiteral(vector),
		query.At,
		query.Region,
		query.ProductCategory,
		riskLevelStrings(query.RiskLevels),
		candidateLimit,
	)
	if err != nil {
		return nil, mapRetrievalError(ctx, err)
	}
	return scanEvidenceRows(ctx, rows)
}

func scanEvidenceRows(ctx context.Context, rows pgx.Rows) ([]Evidence, error) {
	defer rows.Close()
	var evidence []Evidence
	for rows.Next() {
		var item Evidence
		var riskLevel string
		var maxCouponCents pgtype.Int8
		if err := rows.Scan(
			&item.ChunkID,
			&item.PolicyID,
			&item.Version,
			&item.Section,
			&item.StartOffset,
			&item.EndOffset,
			&item.EffectiveFrom,
			&item.EffectiveTo,
			&item.Region,
			&item.ProductCategory,
			&riskLevel,
			&maxCouponCents,
			&item.Text,
		); err != nil {
			return nil, mapRetrievalError(ctx, err)
		}
		item.EffectiveFrom = item.EffectiveFrom.UTC()
		item.EffectiveTo = item.EffectiveTo.UTC()
		item.RiskLevel = toolkit.Risk(riskLevel)
		if maxCouponCents.Valid {
			value := maxCouponCents.Int64
			item.MaxCouponCents = &value
		}
		item.CitationID = citationID(item)
		evidence = append(evidence, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapRetrievalError(ctx, err)
	}
	return evidence, nil
}

func riskLevelStrings(riskLevels []toolkit.Risk) []string {
	values := make([]string, len(riskLevels))
	for index, riskLevel := range riskLevels {
		values[index] = string(riskLevel)
	}
	return values
}

func mapRetrievalError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRetrieval
}

func (store *PostgresStore) Save(ctx context.Context, sourceName string, document Document) (err error) {
	if store == nil || store.pool == nil {
		return ErrStore
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return mapStoreError(ctx, err)
	}
	defer func() {
		rollbackErr := tx.Rollback(ctx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			err = mapStoreError(ctx, rollbackErr)
		}
	}()

	inserted, err := insertPolicyDocument(ctx, tx, sourceName, document)
	if err != nil {
		return mapStoreError(ctx, err)
	}
	if !inserted {
		var existingHash string
		if err := tx.QueryRow(ctx, `
			select content_sha256
			from policy_documents
			where policy_id = $1 and version = $2
			for update
		`, document.Metadata.PolicyID, document.Metadata.Version).Scan(&existingHash); err != nil {
			return mapStoreError(ctx, err)
		}
		if existingHash == document.ContentSHA256 {
			return mapStoreError(ctx, tx.Commit(ctx))
		}
		if err := replacePolicyDocument(ctx, tx, sourceName, document); err != nil {
			return mapStoreError(ctx, err)
		}
	}

	for _, chunk := range document.Chunks {
		if _, err := tx.Exec(ctx, `
			insert into policy_chunks (
				id, policy_id, version, section, content, start_offset, end_offset, embedding
			) values ($1, $2, $3, $4, $5, $6, $7, $8)
		`,
			chunk.ID,
			document.Metadata.PolicyID,
			document.Metadata.Version,
			chunk.Section,
			chunk.Text,
			chunk.StartOffset,
			chunk.EndOffset,
			vectorLiteral(chunk.Embedding),
		); err != nil {
			return mapStoreError(ctx, err)
		}
	}
	return mapStoreError(ctx, tx.Commit(ctx))
}

func insertPolicyDocument(ctx context.Context, tx pgx.Tx, sourceName string, document Document) (bool, error) {
	tag, err := tx.Exec(ctx, `
		insert into policy_documents (
			policy_id, version, source_name, effective_from, effective_to, region,
			product_category, risk_level, max_coupon_cents, content_sha256
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		on conflict (policy_id, version) do nothing
	`,
		document.Metadata.PolicyID,
		document.Metadata.Version,
		sourceName,
		document.Metadata.EffectiveFrom,
		document.Metadata.EffectiveTo,
		document.Metadata.Region,
		document.Metadata.ProductCategory,
		document.Metadata.RiskLevel,
		document.Metadata.MaxCouponCents,
		document.ContentSHA256,
	)
	return tag.RowsAffected() == 1, err
}

func replacePolicyDocument(ctx context.Context, tx pgx.Tx, sourceName string, document Document) error {
	if _, err := tx.Exec(ctx, `
		update policy_documents
		set source_name = $3,
		    effective_from = $4,
		    effective_to = $5,
		    region = $6,
		    product_category = $7,
		    risk_level = $8,
		    max_coupon_cents = $9,
		    content_sha256 = $10,
		    imported_at = now()
		where policy_id = $1 and version = $2
	`,
		document.Metadata.PolicyID,
		document.Metadata.Version,
		sourceName,
		document.Metadata.EffectiveFrom,
		document.Metadata.EffectiveTo,
		document.Metadata.Region,
		document.Metadata.ProductCategory,
		document.Metadata.RiskLevel,
		document.Metadata.MaxCouponCents,
		document.ContentSHA256,
	); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		delete from policy_chunks
		where policy_id = $1 and version = $2
	`, document.Metadata.PolicyID, document.Metadata.Version)
	return err
}

func vectorLiteral(vector []float32) string {
	var builder strings.Builder
	builder.Grow(len(vector)*4 + 2)
	builder.WriteByte('[')
	for index, value := range vector {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

func mapStoreError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrStore
}
