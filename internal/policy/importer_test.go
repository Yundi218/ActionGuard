package policy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestImportSameVersionAndContentIsIdempotent(t *testing.T) {
	store, pool, ctx := newPolicyStoreTest(t)
	importer := NewImporter(DeterministicEmbedder{}, store)
	markdown := policyMarkdown(validFrontMatter, "## Replacement window\n\nReplace eligible damaged electronics.\n")

	if err := importer.Import(ctx, "damaged-goods-v3.md", markdown); err != nil {
		t.Fatal(err)
	}
	first := readPolicyState(t, pool, ctx)
	time.Sleep(10 * time.Millisecond)
	if err := importer.Import(ctx, "duplicate-source.md", markdown); err != nil {
		t.Fatal(err)
	}
	second := readPolicyState(t, pool, ctx)

	if first.documentCount != 1 || second.documentCount != 1 {
		t.Fatalf("document counts = %d then %d, want 1 then 1", first.documentCount, second.documentCount)
	}
	if !first.importedAt.Equal(second.importedAt) {
		t.Fatalf("idempotent import changed imported_at from %v to %v", first.importedAt, second.importedAt)
	}
	if second.sourceName != "damaged-goods-v3.md" {
		t.Fatalf("idempotent import changed source name to %q", second.sourceName)
	}
	if !slices.Equal(first.chunkIDs, second.chunkIDs) {
		t.Fatalf("idempotent import changed chunks from %v to %v", first.chunkIDs, second.chunkIDs)
	}
}

func TestImportChangedContentReplacesChunks(t *testing.T) {
	store, pool, ctx := newPolicyStoreTest(t)
	importer := NewImporter(DeterministicEmbedder{}, store)
	original := policyMarkdown(validFrontMatter, "## Old section\n\nOld policy text.\n")
	changedFrontMatter := strings.Replace(validFrontMatter, "max_coupon_cents: 2000", "max_coupon_cents: 1500", 1)
	changed := policyMarkdown(changedFrontMatter, "## New section\n\nNew policy text.\n\n## Evidence\n\nSecond chunk.\n")

	if err := importer.Import(ctx, "original.md", original); err != nil {
		t.Fatal(err)
	}
	before := readPolicyState(t, pool, ctx)
	if err := importer.Import(ctx, "changed.md", changed); err != nil {
		t.Fatal(err)
	}
	after := readPolicyState(t, pool, ctx)

	parsed, err := ParseMarkdown(changed)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := make([]string, len(parsed.Chunks))
	for index, chunk := range parsed.Chunks {
		wantIDs[index] = chunk.ID
	}
	slices.Sort(wantIDs)
	if after.contentSHA256 != parsed.ContentSHA256 || after.sourceName != "changed.md" || after.maxCouponCents != 1500 {
		t.Fatalf("changed document state = %#v", after)
	}
	if !slices.Equal(after.chunkIDs, wantIDs) {
		t.Fatalf("changed chunk IDs = %v, want %v", after.chunkIDs, wantIDs)
	}
	for _, oldID := range before.chunkIDs {
		if slices.Contains(after.chunkIDs, oldID) {
			t.Fatalf("stale chunk %q survived changed-content import", oldID)
		}
	}
}

func TestImportChangedContentRollsBackDocumentAndChunksTogether(t *testing.T) {
	store, pool, ctx := newPolicyStoreTest(t)
	importer := NewImporter(DeterministicEmbedder{}, store)
	original := policyMarkdown(validFrontMatter, "## Original\n\nOriginal policy text.\n")
	changed := policyMarkdown(validFrontMatter, "## Accepted\n\nThis insert starts.\n\n## Rejected\n\nThis insert must fail.\n")

	if err := importer.Import(ctx, "original.md", original); err != nil {
		t.Fatal(err)
	}
	before := readPolicyState(t, pool, ctx)
	if _, err := pool.Exec(ctx, `
		alter table policy_chunks
		add constraint policy_chunks_reject_section check (section <> 'Rejected')
	`); err != nil {
		t.Fatal(err)
	}

	err := importer.Import(ctx, "changed.md", changed)
	if !errors.Is(err, ErrStore) {
		t.Fatalf("failed replacement error = %v, want ErrStore", err)
	}
	after := readPolicyState(t, pool, ctx)
	if after.contentSHA256 != before.contentSHA256 || after.sourceName != before.sourceName ||
		!after.importedAt.Equal(before.importedAt) || !slices.Equal(after.chunkIDs, before.chunkIDs) {
		t.Fatalf("failed replacement was not rolled back:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestImportRejectsWrongEmbeddingWidthBeforeWriting(t *testing.T) {
	store := &recordingPolicyStore{}
	importer := NewImporter(fixedWidthEmbedder{width: 2}, store)
	err := importer.Import(context.Background(), "damaged.md", policyMarkdown(validFrontMatter, "## Rules\n\nText.\n"))
	if !errors.Is(err, ErrEmbedding) {
		t.Fatalf("embedding width error = %v, want ErrEmbedding", err)
	}
	if store.called {
		t.Fatal("store was called with an invalid embedding")
	}
}

type fixedWidthEmbedder struct {
	width int
}

func (embedder fixedWidthEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = make([]float32, embedder.width)
	}
	return vectors, nil
}

type recordingPolicyStore struct {
	called bool
}

func (store *recordingPolicyStore) Save(context.Context, string, Document) error {
	store.called = true
	return nil
}

type storedPolicyState struct {
	documentCount  int
	contentSHA256  string
	sourceName     string
	maxCouponCents int64
	importedAt     time.Time
	chunkIDs       []string
}

func readPolicyState(t *testing.T, pool *pgxpool.Pool, ctx context.Context) storedPolicyState {
	t.Helper()
	var state storedPolicyState
	if err := pool.QueryRow(ctx, `select count(*) from policy_documents`).Scan(&state.documentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select content_sha256, source_name, coalesce(max_coupon_cents, -1), imported_at
		from policy_documents
		where policy_id = 'damaged_goods' and version = 'v3'
	`).Scan(&state.contentSHA256, &state.sourceName, &state.maxCouponCents, &state.importedAt); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select id from policy_chunks order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		state.chunkIDs = append(state.chunkIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}

func newPolicyStoreTest(t *testing.T) (*PostgresStore, *pgxpool.Pool, context.Context) {
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

	schema := fmt.Sprintf("policy_import_%d", time.Now().UnixNano())
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
	query := migrationURL.Query()
	query.Set("search_path", schema+",public")
	migrationURL.RawQuery = query.Encode()

	pool, err := database.Open(ctx, migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewPostgresStore(pool), pool, ctx
}
