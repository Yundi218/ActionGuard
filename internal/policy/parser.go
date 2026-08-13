package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

var ErrInvalidPolicy = errors.New("invalid policy markdown")

var requiredMetadataKeys = []string{
	"policy_id",
	"version",
	"effective_from",
	"effective_to",
	"region",
	"product_category",
	"risk_level",
}

var allowedMetadataKeys = map[string]struct{}{
	"policy_id":        {},
	"version":          {},
	"effective_from":   {},
	"effective_to":     {},
	"region":           {},
	"product_category": {},
	"risk_level":       {},
	"max_coupon_cents": {},
}

func ParseMarkdown(markdown []byte) (Document, error) {
	if !utf8.Valid(markdown) {
		return Document{}, fmt.Errorf("%w: document is not valid UTF-8", ErrInvalidPolicy)
	}
	values, bodyStart, err := parseFrontMatter(markdown)
	if err != nil {
		return Document{}, err
	}
	metadata, err := parseMetadata(values)
	if err != nil {
		return Document{}, err
	}

	bodyBytes := markdown[bodyStart:]
	contentHash := sha256.Sum256(markdown)
	document := Document{
		Metadata:      metadata,
		Body:          string(bodyBytes),
		ContentSHA256: hex.EncodeToString(contentHash[:]),
	}
	document.Chunks, err = buildChunks(document.Body, metadata, document.ContentSHA256)
	if err != nil {
		return Document{}, err
	}
	return document, nil
}

func ChunkID(policyID, version, section string, startOffset, endOffset int, contentSHA256 string) string {
	payload := policyID + "\x00" + version + "\x00" + section + "\x00" +
		strconv.Itoa(startOffset) + "\x00" + strconv.Itoa(endOffset) + "\x00" + contentSHA256
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func parseFrontMatter(markdown []byte) (map[string]string, int, error) {
	first, next := markdownLine(markdown, 0)
	if string(bytes.TrimSuffix(first, []byte("\r"))) != "---" {
		return nil, 0, fmt.Errorf("%w: front matter must begin with ---", ErrInvalidPolicy)
	}

	values := make(map[string]string)
	for position := next; position <= len(markdown); {
		line, following := markdownLine(markdown, position)
		trimmedLine := strings.TrimSuffix(string(line), "\r")
		if trimmedLine == "---" {
			return values, following, nil
		}
		if following == position {
			break
		}
		parts := strings.SplitN(trimmedLine, ":", 2)
		if len(parts) != 2 {
			return nil, 0, fmt.Errorf("%w: malformed front matter line", ErrInvalidPolicy)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if _, ok := allowedMetadataKeys[key]; !ok {
			return nil, 0, fmt.Errorf("%w: unknown metadata key %q", ErrInvalidPolicy, key)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, 0, fmt.Errorf("%w: duplicate metadata key %q", ErrInvalidPolicy, key)
		}
		values[key] = value
		position = following
	}
	return nil, 0, fmt.Errorf("%w: front matter is not closed", ErrInvalidPolicy)
}

func markdownLine(content []byte, start int) ([]byte, int) {
	if start >= len(content) {
		return content[len(content):], start
	}
	relativeEnd := bytes.IndexByte(content[start:], '\n')
	if relativeEnd < 0 {
		return content[start:], len(content)
	}
	end := start + relativeEnd
	return content[start:end], end + 1
}

func parseMetadata(values map[string]string) (Metadata, error) {
	for _, key := range requiredMetadataKeys {
		if strings.TrimSpace(values[key]) == "" {
			return Metadata{}, fmt.Errorf("%w: metadata key %q is required", ErrInvalidPolicy, key)
		}
	}

	effectiveFrom, err := time.Parse(time.RFC3339, values["effective_from"])
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: effective_from must be RFC3339", ErrInvalidPolicy)
	}
	effectiveTo, err := time.Parse(time.RFC3339, values["effective_to"])
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: effective_to must be RFC3339", ErrInvalidPolicy)
	}
	effectiveFrom = effectiveFrom.UTC()
	effectiveTo = effectiveTo.UTC()
	if !effectiveFrom.Before(effectiveTo) {
		return Metadata{}, fmt.Errorf("%w: effective_from must precede effective_to", ErrInvalidPolicy)
	}

	risk := toolkit.Risk(values["risk_level"])
	if risk != toolkit.Read && risk != toolkit.Write && risk != toolkit.HighRiskWrite {
		return Metadata{}, fmt.Errorf("%w: risk_level is invalid", ErrInvalidPolicy)
	}

	var maxCouponCents *int64
	if value, ok := values["max_coupon_cents"]; ok {
		amount, err := strconv.ParseInt(value, 10, 64)
		if err != nil || amount < 0 {
			return Metadata{}, fmt.Errorf("%w: max_coupon_cents must be a non-negative integer", ErrInvalidPolicy)
		}
		maxCouponCents = &amount
	}

	return Metadata{
		PolicyID:        values["policy_id"],
		Version:         values["version"],
		EffectiveFrom:   effectiveFrom,
		EffectiveTo:     effectiveTo,
		Region:          values["region"],
		ProductCategory: values["product_category"],
		RiskLevel:       risk,
		MaxCouponCents:  maxCouponCents,
	}, nil
}

type markdownSection struct {
	name  string
	start int
	end   int
}

func buildChunks(body string, metadata Metadata, contentSHA256 string) ([]Chunk, error) {
	sections, err := findSections(body)
	if err != nil {
		return nil, err
	}

	var chunks []Chunk
	for _, section := range sections {
		for start := section.start; start < section.end; {
			end := chunkEnd(body, start, section.end)
			chunk := Chunk{
				Metadata:    metadata,
				Section:     section.name,
				Text:        body[start:end],
				StartOffset: start,
				EndOffset:   end,
			}
			chunk.ID = ChunkID(
				metadata.PolicyID,
				metadata.Version,
				chunk.Section,
				chunk.StartOffset,
				chunk.EndOffset,
				contentSHA256,
			)
			chunks = append(chunks, chunk)
			if end == section.end {
				break
			}
			start = utf8StartAtOrAfter(body, end-ChunkOverlapBytes, end)
		}
	}
	return chunks, nil
}

func findSections(body string) ([]markdownSection, error) {
	var sections []markdownSection
	for position := 0; position < len(body); {
		lineEnd := strings.IndexByte(body[position:], '\n')
		next := len(body)
		if lineEnd >= 0 {
			lineEnd += position
			next = lineEnd + 1
		} else {
			lineEnd = len(body)
		}
		line := strings.TrimSuffix(body[position:lineEnd], "\r")
		if strings.HasPrefix(line, "## ") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if name == "" {
				return nil, fmt.Errorf("%w: section heading is empty", ErrInvalidPolicy)
			}
			if len(sections) > 0 {
				sections[len(sections)-1].end = position
			}
			sections = append(sections, markdownSection{name: name, start: position, end: len(body)})
		}
		position = next
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("%w: at least one level-two section is required", ErrInvalidPolicy)
	}
	return sections, nil
}

func chunkEnd(body string, start, sectionEnd int) int {
	end := min(start+MaxChunkBytes, sectionEnd)
	end = utf8StartAtOrBefore(body, end, start)
	if end == sectionEnd {
		return end
	}

	segment := body[start:end]
	if boundary := strings.LastIndex(segment, "\n\n"); boundary >= MaxChunkBytes/2 {
		return start + boundary + 2
	}
	return end
}

func utf8StartAtOrBefore(text string, position, floor int) int {
	if position >= len(text) {
		return len(text)
	}
	for position > floor && !utf8.RuneStart(text[position]) {
		position--
	}
	return position
}

func utf8StartAtOrAfter(text string, position, ceiling int) int {
	if position < 0 {
		position = 0
	}
	for position < ceiling && !utf8.RuneStart(text[position]) {
		position++
	}
	return position
}
