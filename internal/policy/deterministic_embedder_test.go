package policy

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestDeterministicEmbedderReturnsStableNormalized1536Vectors(t *testing.T) {
	embedder := DeterministicEmbedder{}
	texts := []string{"Damaged headphones qualify for replacement", "coupon compensation limit"}

	first, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated deterministic embedding differs")
	}
	if len(first) != len(texts) {
		t.Fatalf("vector count = %d, want %d", len(first), len(texts))
	}
	for index, vector := range first {
		if len(vector) != EmbeddingDimensions {
			t.Fatalf("vector %d width = %d, want %d", index, len(vector), EmbeddingDimensions)
		}
		var squaredNorm float64
		for _, value := range vector {
			squaredNorm += float64(value * value)
		}
		if math.Abs(math.Sqrt(squaredNorm)-1) > 1e-6 {
			t.Fatalf("vector %d L2 norm = %.9f, want 1", index, math.Sqrt(squaredNorm))
		}
	}
}

func TestDeterministicEmbedderTokenizesLowercaseAlphanumericTerms(t *testing.T) {
	embedder := DeterministicEmbedder{}
	vectors, err := embedder.Embed(context.Background(), []string{
		"DAMAGED, Headphones! 30 DAYS.",
		"damaged headphones 30 days",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(vectors[0], vectors[1]) {
		t.Fatal("case and punctuation changed token embeddings")
	}
}

func TestDeterministicEmbedderRejectsTokenlessText(t *testing.T) {
	embedder := DeterministicEmbedder{}
	vectors, err := embedder.Embed(context.Background(), []string{"valid tokens", " -- ... \t\n"})
	if !errors.Is(err, ErrTokenlessText) {
		t.Fatalf("tokenless embedding error = %v, want ErrTokenlessText", err)
	}
	if vectors != nil {
		t.Fatalf("vectors = %#v, want nil on tokenless input", vectors)
	}
}

func TestDeterministicEmbedderPreservesContextErrorForTokenlessText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	vectors, err := (DeterministicEmbedder{}).Embed(ctx, []string{"---"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tokenless embedding error = %v, want context.Canceled", err)
	}
	if vectors != nil {
		t.Fatalf("vectors = %#v, want nil on canceled context", vectors)
	}
}
