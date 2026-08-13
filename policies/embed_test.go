package policyassets

import (
	"slices"
	"strings"
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

func TestDamagedGoodsPolicyMatchesReplacementSKUContract(t *testing.T) {
	var markdown string
	for _, asset := range All() {
		if asset.Name == "damaged-goods-v3.md" {
			markdown = string(asset.Markdown)
			break
		}
	}
	if markdown == "" {
		t.Fatal("damaged-goods-v3.md is not embedded")
	}

	forbidden := []string{
		"replacement SKU must match the ordered SKU",
		"inventory is available for the ordered electronics SKU",
	}
	for _, claim := range forbidden {
		if strings.Contains(markdown, claim) {
			t.Fatalf("damaged-goods policy contradicts Phase 1 replacement behavior: %q", claim)
		}
	}
	for _, required := range []string{
		"requested target SKU must exist",
		"requested target SKU must have available inventory",
	} {
		if !strings.Contains(markdown, required) {
			t.Fatalf("damaged-goods policy is missing replacement requirement %q", required)
		}
	}
}
