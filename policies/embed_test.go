package policyassets

import (
	"slices"
	"testing"

	"github.com/Yundi218/ActionGuard/internal/policy"
)

func TestAllReturnsExactlyThreePoliciesInLexicalFilenameOrder(t *testing.T) {
	assets := All()
	gotNames := make([]string, len(assets))
	for index, asset := range assets {
		gotNames[index] = asset.Name
		if len(asset.Markdown) == 0 {
			t.Fatalf("asset %q is empty", asset.Name)
		}
		if _, err := policy.ParseMarkdown(asset.Markdown); err != nil {
			t.Fatalf("parse asset %q: %v", asset.Name, err)
		}
	}
	wantNames := []string{"customer-care-v1.md", "damaged-goods-v3.md", "refunds-v2.md"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("asset names = %v, want %v", gotNames, wantNames)
	}
}
