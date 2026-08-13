package policy

import (
	"context"
	"errors"
	"math"
	"strings"
)

var (
	ErrEmbedding = errors.New("policy embedding failed")
	ErrStore     = errors.New("policy store failed")
)

type PolicyStore interface {
	Save(context.Context, string, Document) error
}

type Importer struct {
	embedder Embedder
	store    PolicyStore
}

func NewImporter(embedder Embedder, store PolicyStore) *Importer {
	return &Importer{embedder: embedder, store: store}
}

func (importer *Importer) Import(ctx context.Context, sourceName string, markdown []byte) error {
	if importer == nil || importer.embedder == nil || importer.store == nil || strings.TrimSpace(sourceName) == "" {
		return ErrInvalidPolicy
	}
	document, err := ParseMarkdown(markdown)
	if err != nil {
		return err
	}

	texts := make([]string, len(document.Chunks))
	for index, chunk := range document.Chunks {
		texts[index] = chunk.Text
	}
	embeddings, err := importer.embedder.Embed(ctx, texts)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmbedding
	}
	if len(embeddings) != len(document.Chunks) {
		return ErrEmbedding
	}
	for index, embedding := range embeddings {
		if len(embedding) != EmbeddingDimensions || !finiteVector(embedding) {
			return ErrEmbedding
		}
		document.Chunks[index].Embedding = append([]float32(nil), embedding...)
	}

	if err := importer.store.Save(ctx, sourceName, document); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrStore
	}
	return nil
}

func finiteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}
