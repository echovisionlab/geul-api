package contentblock

import (
	"encoding/json"
	"strings"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

const (
	presenceParagraphID = "10000000-0000-4000-8000-000000000181"
	presenceFileID      = "10000000-0000-4000-8000-000000000182"
	presenceTableID     = "10000000-0000-4000-8000-000000000183"
	presenceRowID       = "10000000-0000-4000-8000-000000000184"
	presenceCellID      = "10000000-0000-4000-8000-000000000185"
)

func TestPresentRichTextLocaleValuesPreservesExplicitEmptyAndExactSparseLeaves(t *testing.T) {
	snapshot := Snapshot{
		Document: Document{Profile: "post"},
		Blocks: []BaseBlock{
			{ID: uuid.MustParse(presenceParagraphID), Kind: "paragraph"},
			{ID: uuid.MustParse(presenceFileID), Kind: "file"},
			{ID: uuid.MustParse(presenceTableID), Kind: "table"},
		},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{
				{BlockID: uuid.MustParse(presenceParagraphID), LocalizedData: normalizedLocale(t, "paragraph", `{"paragraph":{"props":{},"content":[]}}`)},
				{BlockID: uuid.MustParse(presenceFileID), LocalizedData: normalizedLocale(t, "file", `{"file":{"props":{"alt":""}}}`)},
				{BlockID: uuid.MustParse(presenceTableID), LocalizedData: normalizedLocale(t, "table", `{"table":{"props":{},"content":{"rows":[{"rowId":"`+presenceRowID+`","cells":[{"cellId":"`+presenceCellID+`","content":[]}] }]}}}`)},
			},
		}},
	}

	targets, err := PresentRichTextLocaleValues(snapshot, "en")
	if err != nil {
		t.Fatalf("project present locale values: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("got %d targets, want 3: %#v; storage=%s | %s | %s", len(targets), targets,
			snapshot.LocaleOverlays[0].Blocks[0].LocalizedData,
			snapshot.LocaleOverlays[0].Blocks[1].LocalizedData,
			snapshot.LocaleOverlays[0].Blocks[2].LocalizedData,
		)
	}
	assertLocaleValueTarget(t, targets[0], presenceParagraphID, "content", nil)
	assertLocaleValueTarget(t, targets[1], presenceFileID, "alt", nil)
	assertLocaleValueTarget(t, targets[2], presenceTableID, "tableContent", [][2]string{
		{"field", "rows"},
		{"item", presenceRowID},
		{"field", "cells"},
		{"item", presenceCellID},
		{"field", "content"},
	})

	missing, err := PresentRichTextLocaleValues(snapshot, "ko")
	if err != nil {
		t.Fatalf("project missing locale values: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing locale projected source fallback targets: %#v", missing)
	}
}

func TestPresentRichTextLocaleValuesLoadsLegacyTableWithoutDurableIdentities(t *testing.T) {
	snapshot := Snapshot{
		Document: Document{Profile: "policy"},
		Blocks: []BaseBlock{
			{ID: uuid.MustParse(presenceParagraphID), Kind: "paragraph"},
			{ID: uuid.MustParse(presenceTableID), Kind: "table"},
		},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{
				{BlockID: uuid.MustParse(presenceParagraphID), LocalizedData: []byte(`{"paragraph":{"props":{},"content":[]}}`)},
				{BlockID: uuid.MustParse(presenceTableID), LocalizedData: []byte(`{"table":{"props":{},"content":{"rows":[{"cells":[{"content":[]}] }]}}}`)},
			},
		}},
	}

	targets, err := PresentRichTextLocaleValues(snapshot, "en")
	if err != nil {
		t.Fatalf("project legacy table locale values: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want only paragraph presence: %#v", len(targets), targets)
	}
	assertLocaleValueTarget(t, targets[0], presenceParagraphID, "content", nil)
}

func TestPresentRichTextLocaleValuesRejectsPartiallyMigratedTableIdentities(t *testing.T) {
	snapshot := Snapshot{
		Document: Document{Profile: "policy"},
		Blocks:   []BaseBlock{{ID: uuid.MustParse(presenceTableID), Kind: "table"}},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{{
				BlockID: uuid.MustParse(presenceTableID),
				LocalizedData: []byte(`{"table":{"props":{},"content":{"rows":[` +
					`{"rowId":"` + presenceRowID + `","cells":[{"cellId":"` + presenceCellID + `","content":[]}]},` +
					`{"cells":[{"content":[]}]}` +
					`]}}}`),
			}},
		}},
	}

	_, err := PresentRichTextLocaleValues(snapshot, "en")
	if err == nil || !strings.Contains(err.Error(), "partially migrated durable identities") {
		t.Fatalf("partially migrated table error = %v", err)
	}
}

func TestPresentRichTextLocaleValuesRejectsDurableRowsWithLegacyCells(t *testing.T) {
	snapshot := Snapshot{
		Document: Document{Profile: "policy"},
		Blocks:   []BaseBlock{{ID: uuid.MustParse(presenceTableID), Kind: "table"}},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{{
				BlockID: uuid.MustParse(presenceTableID),
				LocalizedData: []byte(`{"table":{"props":{},"content":{"rows":[` +
					`{"rowId":"` + presenceRowID + `","cells":[{"content":[]}]}` +
					`]}}}`),
			}},
		}},
	}

	_, err := PresentRichTextLocaleValues(snapshot, "en")
	if err == nil || !strings.Contains(err.Error(), "partially migrated durable identities") {
		t.Fatalf("partially migrated table error = %v", err)
	}
}

func TestRestoreRichTextAffectedLocaleValuesPreservesExplicitProtoDefaults(t *testing.T) {
	storage := contentv1.ContentStorageMutationBatch{
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale: "en",
			Upserts: []contentv1.ContentStorageLocaleUpsert{
				{
					BlockID:       presenceParagraphID,
					ExpectedKind:  "paragraph",
					LocalizedData: []byte(`{"paragraph":{"props":{}}}`),
				},
				{
					BlockID:      presenceTableID,
					ExpectedKind: "table",
					LocalizedData: []byte(`{"table":{"props":{},"content":{"rows":[{"rowId":"` +
						presenceRowID + `","cells":[{"cellId":"` + presenceCellID + `"}]}]}}}`),
				},
			},
		}},
	}
	values := []*managev1.AIDocumentFieldTarget{
		localeValueTarget(presenceParagraphID, "content"),
		localeValueTarget(presenceTableID, "tableContent",
			fieldPath("rows"), itemPath(presenceRowID), fieldPath("cells"), itemPath(presenceCellID), fieldPath("content"),
		),
	}
	if err := RestoreRichTextAffectedLocaleValues(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"en",
		&storage,
		values,
	); err != nil {
		t.Fatalf("restore affected locale values: %v", err)
	}
	paragraph := string(storage.LocaleGroups[0].Upserts[0].LocalizedData)
	if !strings.Contains(paragraph, `"content":[]`) {
		t.Fatalf("explicit empty Paragraph content was lost: %s", paragraph)
	}
	table := string(storage.LocaleGroups[0].Upserts[1].LocalizedData)
	if !strings.Contains(table, `"cellId":"`+presenceCellID+`","content":[]`) {
		t.Fatalf("explicit empty table cell content was lost: %s", table)
	}
}

func TestRestoreRichTextAffectedLocaleValuesRejectsNonCanonicalTargets(t *testing.T) {
	storage := contentv1.ContentStorageMutationBatch{
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale: "en",
			Upserts: []contentv1.ContentStorageLocaleUpsert{{
				BlockID:       presenceParagraphID,
				ExpectedKind:  "paragraph",
				LocalizedData: []byte(`{"paragraph":{"props":{},"content":[]}}`),
			}},
		}},
	}
	duplicate := localeValueTarget(presenceParagraphID, "content")
	err := RestoreRichTextAffectedLocaleValues(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"en",
		&storage,
		[]*managev1.AIDocumentFieldTarget{duplicate, duplicate},
	)
	if err == nil || !strings.Contains(err.Error(), "canonical-sorted and duplicate-free") {
		t.Fatalf("duplicate affected target error = %v", err)
	}
}

func normalizedLocale(t *testing.T, kind, value string) json.RawMessage {
	t.Helper()
	normalized, err := contentv1.NormalizeContentStorageLocale(
		"post",
		kind,
		[]byte(value),
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		t.Fatalf("normalize %s locale: %v", kind, err)
	}
	return normalized
}

func assertLocaleValueTarget(
	t *testing.T,
	target *managev1.AIDocumentFieldTarget,
	blockID string,
	field string,
	path [][2]string,
) {
	t.Helper()
	if target.GetBlockHandle() != blockID || target.GetFieldHandle() != field {
		t.Fatalf("target = %#v, want %s/%s", target, blockID, field)
	}
	if len(target.GetPath()) != len(path) {
		t.Fatalf("target path length = %d, want %d", len(target.GetPath()), len(path))
	}
	for index, want := range path {
		segment := target.GetPath()[index]
		var got [2]string
		switch selector := segment.GetSelector().(type) {
		case *managev1.AIDocumentFieldPathSegment_FieldHandle:
			got = [2]string{"field", selector.FieldHandle}
		case *managev1.AIDocumentFieldPathSegment_ItemHandle:
			got = [2]string{"item", selector.ItemHandle}
		default:
			t.Fatalf("target path %d has no selector", index)
		}
		if got != want {
			t.Fatalf("target path %d = %#v, want %#v", index, got, want)
		}
	}
}
