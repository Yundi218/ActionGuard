package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const validFrontMatter = `policy_id: damaged_goods
version: v3
effective_from: 2026-01-01T08:00:00+08:00
effective_to: 2027-01-01T08:00:00+08:00
region: CN
product_category: electronics
risk_level: write
max_coupon_cents: 2000
`

func TestParseMarkdownRejectsMissingUnknownAndInvalidMetadata(t *testing.T) {
	required := []string{
		"policy_id: damaged_goods\n",
		"version: v3\n",
		"effective_from: 2026-01-01T08:00:00+08:00\n",
		"effective_to: 2027-01-01T08:00:00+08:00\n",
		"region: CN\n",
		"product_category: electronics\n",
		"risk_level: write\n",
	}
	for _, field := range required {
		name := strings.TrimSpace(strings.SplitN(field, ":", 2)[0])
		t.Run("missing_"+name, func(t *testing.T) {
			frontMatter := strings.Replace(validFrontMatter, field, "", 1)
			if _, err := ParseMarkdown(policyMarkdown(frontMatter, "## Rules\n\nSynthetic rule.\n")); err == nil {
				t.Fatalf("ParseMarkdown accepted missing %s", name)
			}
		})
	}

	tests := map[string]string{
		"unknown key":     validFrontMatter + "audience: internal\n",
		"duplicate key":   validFrontMatter + "region: US\n",
		"invalid risk":    strings.Replace(validFrontMatter, "risk_level: write", "risk_level: arbitrary", 1),
		"negative coupon": strings.Replace(validFrontMatter, "max_coupon_cents: 2000", "max_coupon_cents: -1", 1),
	}
	for name, frontMatter := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMarkdown(policyMarkdown(frontMatter, "## Rules\n\nSynthetic rule.\n")); err == nil {
				t.Fatalf("ParseMarkdown accepted %s", name)
			}
		})
	}
}

func TestParseMarkdownNormalizesTimesAndUsesHalfOpenApplicability(t *testing.T) {
	document, err := ParseMarkdown(policyMarkdown(validFrontMatter, "## Rules\n\nSynthetic rule.\n"))
	if err != nil {
		t.Fatal(err)
	}

	wantFrom := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !document.Metadata.EffectiveFrom.Equal(wantFrom) || document.Metadata.EffectiveFrom.Location() != time.UTC {
		t.Fatalf("effective_from = %v (%v), want %v UTC", document.Metadata.EffectiveFrom, document.Metadata.EffectiveFrom.Location(), wantFrom)
	}
	if !document.Metadata.EffectiveTo.Equal(wantTo) || document.Metadata.EffectiveTo.Location() != time.UTC {
		t.Fatalf("effective_to = %v (%v), want %v UTC", document.Metadata.EffectiveTo, document.Metadata.EffectiveTo.Location(), wantTo)
	}
	if document.Metadata.RiskLevel != toolkit.Write {
		t.Fatalf("risk level = %q, want %q", document.Metadata.RiskLevel, toolkit.Write)
	}
	if document.Metadata.MaxCouponCents == nil || *document.Metadata.MaxCouponCents != 2000 {
		t.Fatalf("max coupon cents = %v, want 2000", document.Metadata.MaxCouponCents)
	}

	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "before", at: wantFrom.Add(-time.Nanosecond), want: false},
		{name: "lower boundary", at: wantFrom, want: true},
		{name: "inside", at: wantTo.Add(-time.Nanosecond), want: true},
		{name: "upper boundary", at: wantTo, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := document.Metadata.AppliesAt(test.at); got != test.want {
				t.Fatalf("AppliesAt(%v) = %t, want %t", test.at, got, test.want)
			}
		})
	}
}

func TestParseMarkdownAllowsOmittedOptionalCouponLimit(t *testing.T) {
	frontMatter := strings.Replace(validFrontMatter, "max_coupon_cents: 2000\n", "", 1)
	document, err := ParseMarkdown(policyMarkdown(frontMatter, "## Rules\n\nSynthetic rule.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if document.Metadata.MaxCouponCents != nil {
		t.Fatalf("max coupon cents = %v, want nil", document.Metadata.MaxCouponCents)
	}
}

func TestParseMarkdownRejectsInvalidEffectiveInterval(t *testing.T) {
	for name, effectiveTo := range map[string]string{
		"equal":  "2026-01-01T08:00:00+08:00",
		"before": "2025-12-31T08:00:00+08:00",
	} {
		t.Run(name, func(t *testing.T) {
			frontMatter := strings.Replace(validFrontMatter, "2027-01-01T08:00:00+08:00", effectiveTo, 1)
			if _, err := ParseMarkdown(policyMarkdown(frontMatter, "## Rules\n\nSynthetic rule.\n")); err == nil {
				t.Fatal("ParseMarkdown accepted effective_from >= effective_to")
			}
		})
	}
}

func TestParseMarkdownRejectsInvalidUTF8InFrontMatter(t *testing.T) {
	markdown := policyMarkdown(validFrontMatter, "## Rules\n\nSynthetic rule.\n")
	policyIDStart := strings.Index(string(markdown), "damaged_goods")
	if policyIDStart < 0 {
		t.Fatal("test fixture is missing policy_id value")
	}
	markdown[policyIDStart] = 0xff

	if _, err := ParseMarkdown(markdown); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("ParseMarkdown invalid front-matter UTF-8 error = %v, want ErrInvalidPolicy", err)
	}
}

func TestParseMarkdownBuildsStableSectionChunksWithSourceByteOffsets(t *testing.T) {
	body := "# Damaged goods\n\nOverview.\n\n" +
		"## Replacement window\n\nFirst paragraph.\n\nSecond paragraph.\n\n" +
		"## Compensation\n\nCoupon rules.\n"
	markdown := policyMarkdown(validFrontMatter, body)

	document, err := ParseMarkdown(markdown)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseMarkdown(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if document.Body != body {
		t.Fatalf("body changed during parsing:\n%q\nwant:\n%q", document.Body, body)
	}
	if !reflect.DeepEqual(document.Chunks, again.Chunks) {
		t.Fatal("repeated parsing produced different chunks")
	}
	if len(document.Chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(document.Chunks))
	}

	wantSections := []string{"Replacement window", "Compensation"}
	for index, chunk := range document.Chunks {
		if chunk.Section != wantSections[index] {
			t.Fatalf("chunk %d section = %q, want %q", index, chunk.Section, wantSections[index])
		}
		if chunk.StartOffset < 0 || chunk.EndOffset > len(document.Body) || chunk.StartOffset >= chunk.EndOffset {
			t.Fatalf("chunk %d offsets = [%d,%d), body length %d", index, chunk.StartOffset, chunk.EndOffset, len(document.Body))
		}
		if got := document.Body[chunk.StartOffset:chunk.EndOffset]; got != chunk.Text {
			t.Fatalf("chunk %d text does not match body offsets: %q != %q", index, chunk.Text, got)
		}
		if chunk.Metadata != document.Metadata {
			t.Fatalf("chunk %d metadata differs from document metadata", index)
		}
		wantID := specifiedChunkID(
			document.Metadata.PolicyID,
			document.Metadata.Version,
			chunk.Section,
			chunk.StartOffset,
			chunk.EndOffset,
			document.ContentSHA256,
		)
		if chunk.ID != wantID {
			t.Fatalf("chunk %d ID = %q, want %q", index, chunk.ID, wantID)
		}
	}
	if strings.Contains(document.Chunks[0].Text, "## Compensation") {
		t.Fatal("replacement chunk crosses into compensation section")
	}
	if strings.Contains(document.Chunks[1].Text, "## Replacement window") {
		t.Fatal("compensation chunk crosses into replacement section")
	}

	wantContentHash := sha256.Sum256(markdown)
	if document.ContentSHA256 != hex.EncodeToString(wantContentHash[:]) {
		t.Fatalf("content SHA-256 = %q, want %x", document.ContentSHA256, wantContentHash)
	}
}

func TestParseMarkdownCapsChunksWithUTF8SafeOverlap(t *testing.T) {
	body := "## Long section\n\n" + strings.Repeat("损坏商品需要换货。", 100) + "\n"
	document, err := ParseMarkdown(policyMarkdown(validFrontMatter, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Chunks) < 2 {
		t.Fatalf("chunk count = %d, want multiple chunks", len(document.Chunks))
	}
	for index, chunk := range document.Chunks {
		if len(chunk.Text) > MaxChunkBytes {
			t.Fatalf("chunk %d length = %d, want <= %d", index, len(chunk.Text), MaxChunkBytes)
		}
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d is not valid UTF-8", index)
		}
		if document.Body[chunk.StartOffset:chunk.EndOffset] != chunk.Text {
			t.Fatalf("chunk %d offsets do not map to its text", index)
		}
		if index == 0 {
			continue
		}
		overlap := document.Chunks[index-1].EndOffset - chunk.StartOffset
		if overlap <= 0 || overlap > ChunkOverlapBytes {
			t.Fatalf("chunk %d overlap = %d, want in [1,%d]", index, overlap, ChunkOverlapBytes)
		}
	}
}

func policyMarkdown(frontMatter, body string) []byte {
	return []byte("---\n" + frontMatter + "---\n" + body)
}

func specifiedChunkID(policyID, version, section string, start, end int, contentSHA256 string) string {
	payload := policyID + "\x00" + version + "\x00" + section + "\x00" +
		strconv.Itoa(start) + "\x00" + strconv.Itoa(end) + "\x00" + contentSHA256
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum)
}
