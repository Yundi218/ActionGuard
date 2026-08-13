package policy

import (
	"context"
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
