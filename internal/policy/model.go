package policy

import (
	"time"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const (
	EmbeddingDimensions = 1536
	MaxChunkBytes       = 1200
	ChunkOverlapBytes   = 150
)

type Metadata struct {
	PolicyID        string
	Version         string
	EffectiveFrom   time.Time
	EffectiveTo     time.Time
	Region          string
	ProductCategory string
	RiskLevel       toolkit.Risk
	MaxCouponCents  *int64
}

func (metadata Metadata) AppliesAt(at time.Time) bool {
	at = at.UTC()
	return !at.Before(metadata.EffectiveFrom) && at.Before(metadata.EffectiveTo)
}

type Document struct {
	Metadata      Metadata
	Body          string
	ContentSHA256 string
	Chunks        []Chunk
}

type Chunk struct {
	ID          string
	Metadata    Metadata
	Section     string
	Text        string
	StartOffset int
	EndOffset   int
	Embedding   []float32
}
