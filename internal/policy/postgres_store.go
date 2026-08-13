package policy

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
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
