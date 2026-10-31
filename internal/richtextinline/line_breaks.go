// Package richtextinline owns compatibility normalization of inline line breaks.
package richtextinline

import (
	"bytes"
	"encoding/json"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// NormalizeNewlines maps a legacy CRLF or CR to one LF, without trimming.
func NormalizeNewlines(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// Canonicalize preserves canonical items and splits legacy text/link runs at
// each line break. HardBreak cannot carry styles or be nested in a wire link.
// Input messages are never mutated.
func Canonicalize(content []*contentv1.RichTextInline) []*contentv1.RichTextInline {
	result := make([]*contentv1.RichTextInline, 0, len(content))
	for _, item := range content {
		if text := item.GetText(); text != nil && strings.ContainsAny(text.GetText(), "\r\n") {
			for index, part := range strings.Split(NormalizeNewlines(text.GetText()), "\n") {
				if index > 0 {
					result = append(result, hardBreak())
				}
				if part != "" {
					run := proto.Clone(text).(*contentv1.RichTextStyledText)
					run.Text = part
					result = append(result, &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: run}})
				}
			}
		} else if link := item.GetLink(); link != nil && linkHasNewlines(link) {
			var runs []*contentv1.RichTextStyledText
			flush := func() {
				if len(runs) > 0 {
					split := proto.Clone(link).(*contentv1.RichTextLink)
					split.Content = runs
					result = append(result, &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: split}})
				}
				runs = nil
			}
			for _, text := range link.GetContent() {
				for index, part := range strings.Split(NormalizeNewlines(text.GetText()), "\n") {
					if index > 0 {
						flush()
						result = append(result, hardBreak())
					}
					if part != "" {
						run := proto.Clone(text).(*contentv1.RichTextStyledText)
						run.Text = part
						runs = append(runs, run)
					}
				}
			}
			flush()
		} else {
			result = append(result, item)
		}
	}
	return result
}

func hardBreak() *contentv1.RichTextInline {
	return &contentv1.RichTextInline{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}}
}

func linkHasNewlines(link *contentv1.RichTextLink) bool {
	for _, run := range link.GetContent() {
		if strings.ContainsAny(run.GetText(), "\r\n") {
			return true
		}
	}
	return false
}

// CanonicalizeStorageLocale normalizes only inline content in one submitted
// locale block (including table cells). Plain text/code/source fields and
// canonical JSON bytes are preserved. Call on writes, never on aggregate reads.
func CanonicalizeStorageLocale(data json.RawMessage) (json.RawMessage, error) {
	if bytes.IndexByte(data, '\\') < 0 {
		return data, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	content := root["content"]
	if len(content) == 0 {
		// Durable locale JSON carries the generated block oneof envelope
		// (for example {"paragraph":{"content":...}}). Payloads and table
		// cells use the same normalizer after unwrapping that envelope.
		changed := false
		for key, child := range root {
			if key == "props" || !bytes.HasPrefix(bytes.TrimSpace(child), []byte("{")) {
				continue
			}
			normalized, err := CanonicalizeStorageLocale(child)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(child, normalized) {
				root[key] = normalized
				changed = true
			}
		}
		if changed {
			return json.Marshal(root)
		}
		return data, nil
	}
	changed := false
	if bytes.HasPrefix(bytes.TrimSpace(content), []byte("[")) {
		var raw []json.RawMessage
		if err := json.Unmarshal(content, &raw); err != nil {
			return nil, err
		}
		items := make([]*contentv1.RichTextInline, 0, len(raw))
		for _, entry := range raw {
			item := &contentv1.RichTextInline{}
			if err := protojson.Unmarshal(entry, item); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		canonical := Canonicalize(items)
		for _, item := range items {
			if strings.ContainsAny(item.GetText().GetText(), "\r\n") || linkHasNewlines(item.GetLink()) {
				changed = true
				break
			}
		}
		if changed {
			raw = nil
			for _, item := range canonical {
				entry, err := protojson.Marshal(item)
				if err != nil {
					return nil, err
				}
				raw = append(raw, entry)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, err
			}
			root["content"] = encoded
		}
	} else if bytes.HasPrefix(bytes.TrimSpace(content), []byte("{")) {
		var table map[string]json.RawMessage
		if err := json.Unmarshal(content, &table); err != nil {
			return nil, err
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(table["rows"], &rows); err != nil {
			return data, nil //nolint:nilerr // a table without a decodable rows array has no cells to normalize, so the input is returned unchanged
		}
		for _, row := range rows {
			var cells []json.RawMessage
			if err := json.Unmarshal(row["cells"], &cells); err != nil {
				return nil, err
			}
			for index, cell := range cells {
				normalized, err := CanonicalizeStorageLocale(cell)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(cell, normalized) {
					cells[index] = normalized
					changed = true
				}
			}
			encoded, err := json.Marshal(cells)
			if err != nil {
				return nil, err
			}
			row["cells"] = encoded
		}
		if changed {
			encoded, err := json.Marshal(rows)
			if err != nil {
				return nil, err
			}
			table["rows"] = encoded
			encoded, err = json.Marshal(table)
			if err != nil {
				return nil, err
			}
			root["content"] = encoded
		}
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(root)
}
