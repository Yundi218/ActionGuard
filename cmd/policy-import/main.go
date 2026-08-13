package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/policy"
	policyassets "github.com/Yundi218/ActionGuard/policies"
)

var (
	ErrDatabaseURLRequired    = errors.New("DATABASE_URL is required")
	ErrPolicyImport           = errors.New("policy import failed")
	ErrEmbeddingConfiguration = errors.New("policy embedding configuration invalid")
)

type policyImporter interface {
	Import(context.Context, string, []byte) error
}

func main() {
	providers, err := config.LoadProviderSettings()
	if err == nil {
		err = run(context.Background(), os.Getenv("DATABASE_URL"), providers, os.Stdout)
	}
	if err != nil {
		log.SetFlags(0)
		log.Fatal(err)
	}
}

func run(ctx context.Context, databaseURL string, providers config.ProviderSettings, output io.Writer) error {
	if strings.TrimSpace(databaseURL) == "" {
		return ErrDatabaseURLRequired
	}
	embedder, err := newPolicyEmbedder(providers)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return mapImportError(ctx)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		return mapImportError(ctx)
	}
	importer := policy.NewImporter(embedder, policy.NewPostgresStore(pool))
	count, err := importAssets(ctx, importer, policyassets.All())
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "imported %d policies\n", count); err != nil {
		return mapImportError(ctx)
	}
	return nil
}

func newPolicyEmbedder(settings config.ProviderSettings) (policy.Embedder, error) {
	switch settings.EmbeddingProvider {
	case config.ProviderDeterministic:
		return policy.DeterministicEmbedder{}, nil
	case config.ProviderOpenAI:
		embedder, err := llm.NewOpenAIEmbedder(llm.OpenAIEmbeddingConfig{
			BaseURL: settings.OpenAIBaseURL, APIKey: settings.OpenAIAPIKey, Model: settings.OpenAIEmbeddingModel,
		})
		if err != nil {
			return nil, ErrEmbeddingConfiguration
		}
		return embedder, nil
	default:
		return nil, ErrEmbeddingConfiguration
	}
}

func importAssets(ctx context.Context, importer policyImporter, assets []policyassets.Asset) (int, error) {
	ordered := append([]policyassets.Asset(nil), assets...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Name < ordered[right].Name
	})
	for _, asset := range ordered {
		if err := importer.Import(ctx, asset.Name, asset.Markdown); err != nil {
			return 0, mapImportError(ctx)
		}
	}
	return len(ordered), nil
}

func mapImportError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrPolicyImport
}
