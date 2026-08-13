package policy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"unicode"
)

var ErrTokenlessText = errors.New("deterministic embedding text has no alphanumeric tokens")

type DeterministicEmbedder struct{}

func (DeterministicEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tokens := tokenize(text)
		if len(tokens) == 0 {
			return nil, ErrTokenlessText
		}
		vector := make([]float32, EmbeddingDimensions)
		for _, token := range tokens {
			digest := sha256.Sum256([]byte(token))
			dimension := binary.BigEndian.Uint64(digest[:8]) % EmbeddingDimensions
			sign := float32(1)
			if digest[8]&1 == 1 {
				sign = -1
			}
			vector[dimension] += sign
		}
		normalize(vector)
		vectors[index] = vector
	}
	return vectors, nil
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
}

func normalize(vector []float32) {
	var squaredNorm float64
	for _, value := range vector {
		squaredNorm += float64(value * value)
	}
	if squaredNorm == 0 {
		return
	}
	norm := float32(math.Sqrt(squaredNorm))
	for index := range vector {
		vector[index] /= norm
	}
}
