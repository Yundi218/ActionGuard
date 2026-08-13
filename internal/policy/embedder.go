package policy

import "context"

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}
