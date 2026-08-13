package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/policy"
	policyassets "github.com/Yundi218/ActionGuard/policies"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunMigratesEmptyDatabaseThenImportsAllEmbeddedPolicies(t *testing.T) {
	ctx, databaseURL, pool := newPolicyImportTestDatabase(t)
	var before bool
	if err := pool.QueryRow(ctx, `
		select exists (
			select 1
			from information_schema.tables
			where table_schema = current_schema() and table_name = 'policy_documents'
		)
	`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before {
		t.Fatal("policy_documents exists before the command migration")
	}

	var output bytes.Buffer
	if err := run(ctx, databaseURL, deterministicProviderSettings(), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "imported 3 policies\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	var documentCount, chunkCount int
	if err := pool.QueryRow(ctx, `select count(*) from policy_documents`).Scan(&documentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from policy_chunks`).Scan(&chunkCount); err != nil {
		t.Fatal(err)
	}
	wantChunks := 0
	for _, asset := range policyassets.All() {
		document, err := policy.ParseMarkdown(asset.Markdown)
		if err != nil {
			t.Fatal(err)
		}
		wantChunks += len(document.Chunks)
	}
	if documentCount != len(policyassets.All()) || chunkCount != wantChunks {
		t.Fatalf("imported documents/chunks = %d/%d, want %d/%d", documentCount, chunkCount, len(policyassets.All()), wantChunks)
	}

	var embeddingType string
	if err := pool.QueryRow(ctx, `
		select pg_catalog.format_type(attribute.atttypid, attribute.atttypmod)
		from pg_catalog.pg_attribute attribute
		join pg_catalog.pg_class relation on relation.oid = attribute.attrelid
		join pg_catalog.pg_namespace namespace on namespace.oid = relation.relnamespace
		where namespace.nspname = current_schema()
		  and relation.relname = 'policy_chunks'
		  and attribute.attname = 'embedding'
	`).Scan(&embeddingType); err != nil {
		t.Fatal(err)
	}
	if embeddingType != "vector(1536)" {
		t.Fatalf("embedding column = %q, want migration 002 vector(1536)", embeddingType)
	}
}

func TestRunIsDeterministicAndIdempotent(t *testing.T) {
	ctx, databaseURL, pool := newPolicyImportTestDatabase(t)
	if err := run(ctx, databaseURL, deterministicProviderSettings(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := importedPolicyState(t, ctx, pool)
	time.Sleep(10 * time.Millisecond)
	if err := run(ctx, databaseURL, deterministicProviderSettings(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	second := importedPolicyState(t, ctx, pool)
	if !slices.Equal(first, second) {
		t.Fatalf("repeat import changed policy state:\nfirst:  %v\nsecond: %v", first, second)
	}
	embedder, err := newPolicyEmbedder(deterministicProviderSettings())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := embedder.(policy.DeterministicEmbedder); !ok {
		t.Fatalf("default embedder type = %T, want policy.DeterministicEmbedder", embedder)
	}
}

func TestImportAssetsUsesLexicalFilenameOrder(t *testing.T) {
	assets := policyassets.All()
	slices.Reverse(assets)
	recorder := &recordingImporter{}

	count, err := importAssets(context.Background(), recorder, assets)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"customer-care-v1.md", "damaged-goods-v3.md", "refunds-v2.md"}
	if count != len(want) || !slices.Equal(recorder.names, want) {
		t.Fatalf("imported %d assets in order %v, want %d assets in order %v", count, recorder.names, len(want), want)
	}
}

func TestRunRequiresDatabaseConfiguration(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), " \t", deterministicProviderSettings(), &output)
	if !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("error = %v, want ErrDatabaseURLRequired", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

func TestRunAndImportErrorsAreSanitized(t *testing.T) {
	const secret = "secret-user:secret-password@private-db"
	var output bytes.Buffer
	err := run(context.Background(), "postgres://"+secret+"/%gh", deterministicProviderSettings(), &output)
	if !errors.Is(err, ErrPolicyImport) {
		t.Fatalf("database error = %v, want ErrPolicyImport", err)
	}
	if strings.Contains(err.Error(), secret) || output.Len() != 0 {
		t.Fatalf("database failure leaked sensitive data: error=%q output=%q", err, output.String())
	}

	sensitivePolicy := []byte("confidential policy body")
	_, err = importAssets(context.Background(), failingImporter{err: errors.New(secret)}, []policyassets.Asset{{Name: "secret.md", Markdown: sensitivePolicy}})
	if !errors.Is(err, ErrPolicyImport) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), string(sensitivePolicy)) {
		t.Fatalf("import error was not sanitized: %v", err)
	}
}

func TestNewPolicyEmbedderUsesOpenAIOnlyInExplicitCompleteMode(t *testing.T) {
	settings := config.ProviderSettings{
		EmbeddingProvider:    config.ProviderOpenAI,
		OpenAIBaseURL:        "https://openai.example.test",
		OpenAIAPIKey:         "secret-key",
		OpenAIEmbeddingModel: "embedding-model",
	}
	embedder, err := newPolicyEmbedder(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := embedder.(*llm.OpenAIEmbedder); !ok {
		t.Fatalf("embedder type = %T, want *llm.OpenAIEmbedder", embedder)
	}
}

func TestNewPolicyEmbedderRejectsUnknownAndIncompleteOpenAIModes(t *testing.T) {
	tests := []config.ProviderSettings{
		{EmbeddingProvider: "unknown"},
		{EmbeddingProvider: config.ProviderOpenAI, OpenAIAPIKey: "secret-key", OpenAIEmbeddingModel: "embedding-model", OpenAIBaseURL: "://secret-endpoint"},
		{EmbeddingProvider: config.ProviderOpenAI, OpenAIEmbeddingModel: "embedding-model"},
		{EmbeddingProvider: config.ProviderOpenAI, OpenAIAPIKey: "secret-key"},
	}
	for _, settings := range tests {
		_, err := newPolicyEmbedder(settings)
		if !errors.Is(err, ErrEmbeddingConfiguration) {
			t.Fatalf("settings %#v error = %v, want ErrEmbeddingConfiguration", settings, err)
		}
		if strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "secret-endpoint") {
			t.Fatalf("configuration error leaked sensitive value: %v", err)
		}
	}
}

func deterministicProviderSettings() config.ProviderSettings {
	return config.ProviderSettings{
		LLMProvider:       config.ProviderDeterministic,
		EmbeddingProvider: config.ProviderDeterministic,
		OpenAIBaseURL:     config.DefaultOpenAIBaseURL,
	}
}

type recordingImporter struct {
	names []string
}

func (importer *recordingImporter) Import(_ context.Context, sourceName string, _ []byte) error {
	importer.names = append(importer.names, sourceName)
	return nil
}

type failingImporter struct {
	err error
}

func (importer failingImporter) Import(context.Context, string, []byte) error {
	return importer.err
}

func newPolicyImportTestDatabase(t *testing.T) (context.Context, string, *pgxpool.Pool) {
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

	schema := fmt.Sprintf("policy_command_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	parameters := parsedURL.Query()
	parameters.Set("search_path", schema+",public")
	parsedURL.RawQuery = parameters.Encode()
	pool, err := database.Open(ctx, parsedURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, parsedURL.String(), pool
}

func importedPolicyState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		select document.policy_id, document.version, document.source_name,
		       document.content_sha256, document.imported_at::text, chunk.id, chunk.embedding::text
		from policy_documents document
		join policy_chunks chunk
		  on chunk.policy_id = document.policy_id and chunk.version = document.version
		order by document.source_name, chunk.id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var state []string
	for rows.Next() {
		var values [7]string
		if err := rows.Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6]); err != nil {
			t.Fatal(err)
		}
		state = append(state, strings.Join(values[:], "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}
