package application

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestValidateXLIFFExportSelectionRequiresExplicitModeContract(t *testing.T) {
	patch := managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH
	replace := managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE
	if _, err := validateXLIFFExportSelection(patch, nil); err == nil {
		t.Fatal("PATCH accepted no unit selection")
	}
	if _, err := validateXLIFFExportSelection(replace, []string{"entity:title"}); err == nil {
		t.Fatal("REPLACE accepted a partial unit selection")
	}
	got, err := validateXLIFFExportSelection(patch, []string{"entity:title", "body:block-a"})
	if err != nil || len(got) != 2 {
		t.Fatalf("PATCH selection = (%v, %v)", got, err)
	}
	if _, err := validateXLIFFExportSelection(patch, []string{"entity:title", "entity:title"}); err == nil {
		t.Fatal("PATCH accepted duplicate unit handles")
	}
}

func TestSelectXLIFFUnitsPreservesOrderAcrossNonAdjacentGroups(t *testing.T) {
	document := translation.XLIFFDocument{
		Version: translation.XLIFFVersion, SourceLocale: "ko", TargetLocale: "en",
		File: translation.XLIFFFile{ID: "post:post-a", Groups: []translation.XLIFFGroup{
			{ID: "first", TranslationUnit: []translation.XLIFFUnit{{ID: "a", Source: "A"}}},
			{ID: "middle", TranslationUnit: []translation.XLIFFUnit{{ID: "b", Source: "B"}}},
			{ID: "last", TranslationUnit: []translation.XLIFFUnit{{ID: "c", Source: "C"}}},
		}},
	}
	selected, err := selectXLIFFUnits(
		document,
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		[]string{"a", "c"},
	)
	if err != nil {
		t.Fatalf("selectXLIFFUnits() error = %v", err)
	}
	if len(selected.File.Groups) != 2 || selected.File.Groups[0].ID != "first" || selected.File.Groups[1].ID != "last" {
		t.Fatalf("selected groups = %#v", selected.File.Groups)
	}
	if len(document.File.Groups) != 3 || document.File.Groups[1].ID != "middle" {
		t.Fatalf("source document was mutated: %#v", document.File.Groups)
	}
}

func TestValidateImportedXLIFFRejectsUnitMissingFromCurrentGraph(t *testing.T) {
	plan := &translation.ExtractionPlan{
		EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
		Units: []translation.Unit{
			{UnitID: "kept", SourceText: "현재", SourceLocale: "ko"},
			{UnitID: "new", SourceText: "새 단위", SourceLocale: "ko"},
		},
		Bundles: []translation.Bundle{{
			BundleID: "body", EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
			Units: []translation.Unit{
				{UnitID: "kept", SourceText: "현재", SourceLocale: "ko"},
				{UnitID: "new", SourceText: "새 단위", SourceLocale: "ko"},
			},
		}},
	}
	keptTarget := "Current"
	deletedTarget := "Deleted"
	imported := translation.XLIFFDocument{
		Version: translation.XLIFFVersion, SourceLocale: "ko", TargetLocale: "en",
		File: translation.XLIFFFile{ID: "post:post-a", Groups: []translation.XLIFFGroup{{
			ID: "body", TranslationUnit: []translation.XLIFFUnit{
				{ID: "kept", Source: "내보낼 때의 이전 원문", Target: &keptTarget},
				{ID: "deleted", Source: "삭제됨", Target: &deletedTarget},
			},
		}}},
	}

	if _, _, err := validateImportedXLIFFAgainstCurrentPlan(
		imported, plan, managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("XLIFF unit missing from the current graph error = %v, want invalid argument", err)
	}
}

func TestValidateImportedXLIFFUsesCurrentInlineAuthority(t *testing.T) {
	plan := &translation.ExtractionPlan{
		EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
		Units: []translation.Unit{{UnitID: "kept", SourceText: "현재", SourceLocale: "ko"}},
		Bundles: []translation.Bundle{{
			BundleID: "body", EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
			Units: []translation.Unit{{UnitID: "kept", SourceText: "현재", SourceLocale: "ko"}},
		}},
	}
	target := "Current"
	imported := translation.XLIFFDocument{
		Version: translation.XLIFFVersion, SourceLocale: "ko", TargetLocale: "en",
		File: translation.XLIFFFile{ID: "post:post-a", Groups: []translation.XLIFFGroup{{
			ID: "body", TranslationUnit: []translation.XLIFFUnit{{
				ID: "kept", Source: "내보낼 때의 이전 원문", Target: &target,
			}},
		}}},
	}

	results, handles, err := validateImportedXLIFFAgainstCurrentPlan(
		imported, plan, managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
	)
	if err != nil {
		t.Fatalf("validateImportedXLIFFAgainstCurrentPlan() error = %v", err)
	}
	if len(results) != 1 || results["kept"].TranslatedText != target {
		t.Fatalf("results = %#v, want the current stable unit", results)
	}
	if len(handles) != 1 || handles[0] != "kept" {
		t.Fatalf("handles = %v, want [kept]", handles)
	}
}

func TestValidateImportedXLIFFRequiresModeSpecificManifest(t *testing.T) {
	plan := &translation.ExtractionPlan{
		EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
		Units: []translation.Unit{
			{UnitID: "title", SourceText: "제목", SourceLocale: "ko"},
			{UnitID: "body", SourceText: "본문", SourceLocale: "ko"},
		},
		Bundles: []translation.Bundle{
			{BundleID: "title", EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en", Units: []translation.Unit{{UnitID: "title", SourceText: "제목", SourceLocale: "ko"}}},
			{BundleID: "body", EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en", Units: []translation.Unit{{UnitID: "body", SourceText: "본문", SourceLocale: "ko"}}},
		},
	}
	title := "Title"
	partial := translation.XLIFFDocument{
		Version: translation.XLIFFVersion, SourceLocale: "ko", TargetLocale: "en",
		File: translation.XLIFFFile{ID: "post:post-a", Groups: []translation.XLIFFGroup{{
			ID: "title", TranslationUnit: []translation.XLIFFUnit{{ID: "title", Source: "제목", Target: &title}},
		}}},
	}
	if _, _, err := validateImportedXLIFFAgainstCurrentPlan(
		partial, plan, managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("partial REPLACE error = %v, want invalid argument", err)
	}
	if _, _, err := validateImportedXLIFFAgainstCurrentPlan(
		translation.XLIFFDocument{Version: translation.XLIFFVersion, SourceLocale: "ko", TargetLocale: "en", File: translation.XLIFFFile{ID: "post:post-a"}},
		plan, managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty PATCH error = %v, want invalid argument", err)
	}

	body := "Body"
	complete := partial
	complete.File.Groups = append(complete.File.Groups, translation.XLIFFGroup{
		ID: "body", TranslationUnit: []translation.XLIFFUnit{{ID: "body", Source: "본문", Target: &body}},
	})
	results, handles, err := validateImportedXLIFFAgainstCurrentPlan(
		complete, plan, managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	)
	if err != nil || len(results) != 2 || len(handles) != 2 {
		t.Fatalf("complete REPLACE = (%v, %v, %v)", results, handles, err)
	}
}

func TestValidateTranslationInterchangeTargetStateRejectsPersistenceDrift(t *testing.T) {
	plan := &translation.ExtractionPlan{Units: []translation.Unit{{UnitID: "known"}}}
	for name, state := range map[string]TranslationInterchangeTargetState{
		"missing with revision":    {Revision: "tr1_bad"},
		"present without revision": {Exists: true},
		"unknown unit": {
			Exists: true, Revision: "tr1_current",
			Targets: map[string]translation.UnitResult{"unknown": {UnitID: "unknown"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateTranslationInterchangeTargetState(state, plan); err == nil {
				t.Fatal("invalid owning-domain target state was accepted")
			}
		})
	}
}

func TestValidateVerifiedTranslationXLIFFAcceptsExistingUploadExtensions(t *testing.T) {
	const fileID = "018f2ad4-e12a-7d91-9ae6-317551885d9a"
	for _, extension := range []string{"xlf", "xliff", "xml", "bin"} {
		upload := VerifiedTranslationXLIFF{
			FileID: fileID, Extension: extension,
			MimeType: "application/xliff+xml", Body: []byte("<xliff/>"),
		}
		if err := validateVerifiedTranslationXLIFF(upload, fileID); err != nil {
			t.Fatalf("extension %q rejected: %v", extension, err)
		}
	}
	if err := validateVerifiedTranslationXLIFF(VerifiedTranslationXLIFF{
		FileID: fileID, Extension: "pdf", MimeType: "application/xliff+xml", Body: []byte("<xliff/>"),
	}, fileID); err == nil {
		t.Fatal("unrelated File extension was accepted")
	}
}
